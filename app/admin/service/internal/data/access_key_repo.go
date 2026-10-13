package data

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	paginationV1 "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
	"go-wind-admin/pkg/localdeps/go-utils/copierutil"
	"go-wind-admin/pkg/localdeps/go-utils/mapper"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"

	"entgo.io/ent/privacy"
	kerrors "github.com/go-kratos/kratos/v2/errors"
	"google.golang.org/protobuf/types/known/timestamppb"

	accesskeyV1 "go-wind-admin/api/gen/go/access_key/service/v1"
	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/accesskey"
	"go-wind-admin/app/admin/service/internal/data/ent/accesskeyidempotency"
	"go-wind-admin/app/admin/service/internal/data/ent/predicate"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	appcrypto "go-wind-admin/pkg/crypto"
	"go-wind-admin/pkg/middleware/auth"
)

// AccessKeyCreateAction 固定为访问凭证创建，参与幂等自然键，避免与其它动作串键。
const AccessKeyCreateAction = "access_key.create"

var (
	// ErrAccessKeyIdempotencyConflict 同键不同意图：客户端复用了幂等键但改了报文。
	ErrAccessKeyIdempotencyConflict = errors.New("access key idempotency conflict")
	// ErrAccessKeyIdempotencyMiss 该幂等键在本作用域内尚无记录，调用方可继续创建。
	ErrAccessKeyIdempotencyMiss = errors.New("access key idempotency record not found")
	// errAccessKeyIdempotencyRace 并发同键插入撞唯一索引，需回退后重读回放。
	errAccessKeyIdempotencyRace = errors.New("access key idempotency race")
)

// AccessKeyCreateFingerprint 计算一次创建意图的规范指纹（sha256 十六进制）。
// 只覆盖输入侧元数据；不含 idempotency_key 自身，因此"同键不同意图"可判定。
func AccessKeyCreateFingerprint(data *accesskeyV1.CreateAccessKeyData) string {
	expires := ""
	if ts := data.GetExpiresAt(); ts != nil {
		expires = ts.AsTime().UTC().Format(time.RFC3339Nano)
	}
	// strconv.Quote 让 name 成为无歧义字段，避免分隔符被名字内容伪造。
	payload := strings.Join([]string{
		AccessKeyCreateAction,
		strconv.Quote(data.GetName()),
		strconv.FormatUint(uint64(data.GetRoleId()), 10),
		expires,
	}, "\x1f")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

type AccessKeyRepo struct {
	entClient  *entCrud.EntClient[*ent.Client]
	cipher     *appcrypto.AccessKeyCipher
	repository *entCrud.Repository[ent.AccessKeyQuery, ent.AccessKeySelect, ent.AccessKeyCreate, ent.AccessKeyCreateBulk, ent.AccessKeyUpdate, ent.AccessKeyUpdateOne, ent.AccessKeyDelete, predicate.AccessKey, accesskeyV1.AccessKey, ent.AccessKey]
}

func NewAccessKeyRepo(_ *bootstrap.Context, client *entCrud.EntClient[*ent.Client], cipher *appcrypto.AccessKeyCipher) *AccessKeyRepo {
	m := mapper.NewCopierMapper[accesskeyV1.AccessKey, ent.AccessKey]()
	m.AppendConverters(copierutil.NewTimeStringConverterPair())
	m.AppendConverters(copierutil.NewTimeTimestamppbConverterPair())
	return &AccessKeyRepo{entClient: client, cipher: cipher, repository: entCrud.NewRepository[ent.AccessKeyQuery, ent.AccessKeySelect, ent.AccessKeyCreate, ent.AccessKeyCreateBulk, ent.AccessKeyUpdate, ent.AccessKeyUpdateOne, ent.AccessKeyDelete, predicate.AccessKey, accesskeyV1.AccessKey, ent.AccessKey](m)}
}

// roleBrief 是回填到 AccessKey 读模型上的角色展示信息。
type roleBrief struct {
	name        *string
	description *string
}

// roleBriefByIDs 在请求上下文内（租户边界由 TenantPrivacy 注入）批量读取角色名称/描述。
// 找不到的角色不回填、不报错：Key 与角色的引用关系由创建/更新时的 validKeyRole 保证，
// 读取侧只做展示，角色被停用或删除不应让列表整体失败。
func (r *AccessKeyRepo) roleBriefByIDs(ctx context.Context, roleIDs []uint32) (map[uint32]roleBrief, error) {
	briefs := make(map[uint32]roleBrief, len(roleIDs))
	// 去重并剔除 0，保持查询参数干净；无有效 ID 时不发查询。
	ids := make([]uint32, 0, len(roleIDs))
	seen := make(map[uint32]struct{}, len(roleIDs))
	for _, id := range roleIDs {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return briefs, nil
	}
	roles, err := r.entClient.Client().Role.Query().Where(role.IDIn(ids...)).All(ctx)
	if err != nil {
		return nil, keyRepoError(err)
	}
	for _, e := range roles {
		briefs[e.ID] = roleBrief{name: e.Name, description: e.Description}
	}
	return briefs, nil
}

// accessKeyDTO 构造读模型 DTO，并按 role_id 回填角色名称/描述（角色缺失则字段缺席）。
func accessKeyDTO(e *ent.AccessKey, roles map[uint32]roleBrief) *accesskeyV1.AccessKey {
	dto := &accesskeyV1.AccessKey{Id: trans.Ptr(e.ID), Name: e.Name, AccessKey: trans.Ptr(e.AccessKey), RoleId: trans.Ptr(e.RoleID), IsActive: trans.Ptr(e.Status != nil && *e.Status == accesskey.StatusOn), ExpiresAt: keyTimestamp(e.ExpiresAt), CreatedAt: keyTimestamp(e.CreatedAt), LastUsedAt: keyTimestamp(e.LastUsedAt)}
	if b, ok := roles[e.RoleID]; ok {
		if b.name != nil {
			dto.RoleName = b.name
		}
		if b.description != nil {
			dto.RoleDescription = b.description
		}
	}
	return dto
}

// buildAccessKeyDTO 供单条构造点使用：先按 RoleID 查角色名称/描述，再构造 DTO。
func (r *AccessKeyRepo) buildAccessKeyDTO(ctx context.Context, e *ent.AccessKey) (*accesskeyV1.AccessKey, error) {
	briefs, err := r.roleBriefByIDs(ctx, []uint32{e.RoleID})
	if err != nil {
		return nil, err
	}
	return accessKeyDTO(e, briefs), nil
}

func keyTimestamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}
func keyRepoError(err error) error {
	if ent.IsNotFound(err) {
		return kerrors.NotFound("ACCESS_KEY_NOT_FOUND", "access key not found")
	}
	return kerrors.ServiceUnavailable("ACCESS_KEY_STORAGE_UNAVAILABLE", "access key storage unavailable").WithCause(err)
}
func (r *AccessKeyRepo) List(ctx context.Context, req *paginationV1.PagingRequest) (*accesskeyV1.ListAccessKeyResponse, error) {
	if req == nil {
		return nil, adminV1.ErrorBadRequest("invalid parameter")
	}
	// 分页方式判定：cursor 与 page/pageSize/offset/token 互斥；不带任何窗口参数时
	// 走游标分页（limit 默认 20），沿用 PagingRequest 的过滤/排序/fieldMask 能力。
	// 互斥检查放在 noPaging 之外，noPaging 与 cursor 同时出现同样拒绝。
	if err := checkCursorWindowConflict(req, adminV1.ErrorBadRequest); err != nil {
		return nil, err
	}
	if shouldUseCursorPaging(req) {
		return r.listAccessKeysWithCursor(ctx, req)
	}

	builder := r.entClient.Client().AccessKey.Query()
	// 取页时强制读入 id：下方要按 ID 二次查询实体映射 is_active。
	// fieldMask 若不含 id，取页结果的 ID 会是零值，导致 IDIn(0,...) 命不中、整页为空。
	ret, err := r.repository.ListWithPaging(ctx, builder, builder.Clone(), withFieldMaskIncludingID(req))
	if err != nil {
		return nil, err
	}
	result := &accesskeyV1.ListAccessKeyResponse{Items: []*accesskeyV1.AccessKey{}}
	if ret == nil {
		return result, nil
	}
	result.Total = ret.Total
	// Fetch only the selected IDs to map the internal ON/OFF status explicitly.
	// The generic mapper cannot map ON/OFF to the public is_active boolean.
	ids := make([]uint32, 0, len(ret.Items))
	for _, item := range ret.Items {
		ids = append(ids, item.GetId())
	}
	if len(ids) == 0 {
		return result, nil
	}
	entities, err := r.entClient.Client().AccessKey.Query().Where(accesskey.IDIn(ids...)).All(ctx)
	if err != nil {
		return nil, keyRepoError(err)
	}
	byID := make(map[uint32]*ent.AccessKey, len(entities))
	roleIDs := make([]uint32, 0, len(entities))
	for _, e := range entities {
		byID[e.ID] = e
		roleIDs = append(roleIDs, e.RoleID)
	}
	roles, err := r.roleBriefByIDs(ctx, roleIDs)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if e := byID[id]; e != nil {
			result.Items = append(result.Items, accessKeyDTO(e, roles))
		}
	}
	return result, nil
}
func (r *AccessKeyRepo) Get(ctx context.Context, req *accesskeyV1.GetAccessKeyRequest) (*accesskeyV1.AccessKey, error) {
	if req == nil || req.GetKeyId() == 0 {
		return nil, adminV1.ErrorBadRequest("key_id is required")
	}
	e, err := r.entClient.Client().AccessKey.Query().Where(accesskey.IDEQ(req.GetKeyId())).Only(ctx)
	if err != nil {
		return nil, keyRepoError(err)
	}
	return r.buildAccessKeyDTO(ctx, e)
}
func validKeyRole(ctx context.Context, client *ent.Client, tenantID, roleID uint32) error {
	if tenantID == 0 || roleID == 0 {
		return adminV1.ErrorForbidden("an enabled role in the current tenant is required")
	}
	e, err := client.Role.Query().Where(role.IDEQ(roleID), role.TenantIDEQ(tenantID), role.TypeEQ(role.TypeTenant), role.StatusEQ(role.StatusOn)).Only(ctx)
	if ent.IsNotFound(err) {
		return adminV1.ErrorForbidden("an enabled role in the current tenant is required")
	}
	if err != nil {
		return keyRepoError(err)
	}
	if e.Code == nil || *e.Code == "" {
		return adminV1.ErrorForbidden("role has no authorization identity")
	}
	return nil
}

// Create 写入一把新凭证，不带幂等。调用方在请求未携带 idempotency_key 时使用此路径。
func (r *AccessKeyRepo) Create(ctx context.Context, data *accesskeyV1.CreateAccessKeyData, tenantID, userID uint32, ak, sk string) (*accesskeyV1.AccessKey, error) {
	ciphertext, err := r.cipher.Encrypt(sk)
	if err != nil {
		return nil, keyRepoError(err)
	}
	tx, err := r.entClient.Client().Tx(ctx)
	if err != nil {
		return nil, keyRepoError(err)
	}
	defer tx.Rollback()
	if err = validKeyRole(ctx, tx.Client(), tenantID, data.GetRoleId()); err != nil {
		return nil, err
	}
	e, err := createAccessKeyRow(ctx, tx, data, tenantID, userID, ak, ciphertext)
	if err != nil {
		return nil, keyRepoError(err)
	}
	if err = tx.Commit(); err != nil {
		return nil, keyRepoError(err)
	}
	return r.buildAccessKeyDTO(ctx, e)
}

// CreateIdempotent 在事务内实现输入侧幂等：
//   - 同 (tenant, actor, action, key) 且同指纹 → 回放原对象，replayed=true；
//   - 同键但指纹不同 → ErrAccessKeyIdempotencyConflict（调用方映射 409）；
//   - 未命中 → 校验角色后同时写入凭证行与幂等记录，replayed=false。
//
// 并发同键由唯一索引 uix_sys_access_key_idempotency_key 兜底：落败方命中约束错误，
// 回退后重读已提交记录按回放处理，保证同键最多一把凭证。
func (r *AccessKeyRepo) CreateIdempotent(ctx context.Context, data *accesskeyV1.CreateAccessKeyData, tenantID, userID uint32, ak, sk string) (*accesskeyV1.AccessKey, bool, error) {
	key := strings.TrimSpace(data.GetIdempotencyKey())
	if key == "" {
		return nil, false, adminV1.ErrorBadRequest("idempotency_key is required for the idempotent path")
	}
	fingerprint := AccessKeyCreateFingerprint(data)
	ciphertext, err := r.cipher.Encrypt(sk)
	if err != nil {
		return nil, false, keyRepoError(err)
	}
	dto, replayed, err := r.createIdempotentOnce(ctx, data, tenantID, userID, ak, ciphertext, key, fingerprint)
	if errors.Is(err, errAccessKeyIdempotencyRace) {
		// 并发同键：胜出方已提交，重读其记录按同意图回放（指纹不符仍是冲突）。
		return r.replayIdempotent(ctx, tenantID, userID, key, fingerprint)
	}
	return dto, replayed, err
}

func (r *AccessKeyRepo) createIdempotentOnce(ctx context.Context, data *accesskeyV1.CreateAccessKeyData, tenantID, userID uint32, ak, ciphertext, key, fingerprint string) (*accesskeyV1.AccessKey, bool, error) {
	tx, err := r.entClient.Client().Tx(ctx)
	if err != nil {
		return nil, false, keyRepoError(err)
	}
	defer tx.Rollback()
	row, err := tx.AccessKeyIdempotency.Query().Where(
		accesskeyidempotency.TenantIDEQ(tenantID),
		accesskeyidempotency.ActorIDEQ(userID),
		accesskeyidempotency.ActionEQ(AccessKeyCreateAction),
		accesskeyidempotency.IdempotencyKeyEQ(key),
	).Only(ctx)
	if err == nil {
		return r.replayTx(ctx, tx, row, tenantID, fingerprint)
	}
	if !ent.IsNotFound(err) {
		return nil, false, keyRepoError(err)
	}
	if err = validKeyRole(ctx, tx.Client(), tenantID, data.GetRoleId()); err != nil {
		return nil, false, err
	}
	e, err := createAccessKeyRow(ctx, tx, data, tenantID, userID, ak, ciphertext)
	if err != nil {
		return nil, false, keyRepoError(err)
	}
	if _, err = tx.AccessKeyIdempotency.Create().
		SetTenantID(tenantID).
		SetActorID(userID).
		SetAction(AccessKeyCreateAction).
		SetIdempotencyKey(key).
		SetRequestFingerprint(fingerprint).
		SetAccessKeyID(e.ID).
		SetCreatedAt(time.Now()).
		Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, false, errAccessKeyIdempotencyRace
		}
		return nil, false, keyRepoError(err)
	}
	if err = tx.Commit(); err != nil {
		return nil, false, keyRepoError(err)
	}
	dto, err := r.buildAccessKeyDTO(ctx, e)
	if err != nil {
		return nil, false, err
	}
	return dto, false, nil
}

// replayTx 在已开启的事务内校验指纹并读回原凭证行，同意图时提交并标记回放。
func (r *AccessKeyRepo) replayTx(ctx context.Context, tx *ent.Tx, row *ent.AccessKeyIdempotency, tenantID uint32, fingerprint string) (*accesskeyV1.AccessKey, bool, error) {
	if row.RequestFingerprint != fingerprint {
		return nil, false, ErrAccessKeyIdempotencyConflict
	}
	original, err := tx.AccessKey.Query().Where(accesskey.IDEQ(row.AccessKeyID), accesskey.TenantIDEQ(tenantID)).Only(ctx)
	if ent.IsNotFound(err) {
		// 独立记录比凭证行活得久：原行被物理删除后重放不再有可回放对象。
		return nil, false, kerrors.NotFound("ACCESS_KEY_NOT_FOUND", "access key not found")
	}
	if err != nil {
		return nil, false, keyRepoError(err)
	}
	if err = tx.Commit(); err != nil {
		return nil, false, keyRepoError(err)
	}
	dto, err := r.buildAccessKeyDTO(ctx, original)
	if err != nil {
		return nil, false, err
	}
	return dto, true, nil
}

func (r *AccessKeyRepo) replayIdempotent(ctx context.Context, tenantID, userID uint32, key, fingerprint string) (*accesskeyV1.AccessKey, bool, error) {
	row, err := r.entClient.Client().AccessKeyIdempotency.Query().Where(
		accesskeyidempotency.TenantIDEQ(tenantID),
		accesskeyidempotency.ActorIDEQ(userID),
		accesskeyidempotency.ActionEQ(AccessKeyCreateAction),
		accesskeyidempotency.IdempotencyKeyEQ(key),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, false, keyRepoError(err)
	}
	if err != nil {
		return nil, false, keyRepoError(err)
	}
	if row.RequestFingerprint != fingerprint {
		return nil, false, ErrAccessKeyIdempotencyConflict
	}
	original, err := r.entClient.Client().AccessKey.Query().Where(accesskey.IDEQ(row.AccessKeyID), accesskey.TenantIDEQ(tenantID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, false, kerrors.NotFound("ACCESS_KEY_NOT_FOUND", "access key not found")
	}
	if err != nil {
		return nil, false, keyRepoError(err)
	}
	dto, err := r.buildAccessKeyDTO(ctx, original)
	if err != nil {
		return nil, false, err
	}
	return dto, true, nil
}

func createAccessKeyRow(ctx context.Context, tx *ent.Tx, data *accesskeyV1.CreateAccessKeyData, tenantID, userID uint32, ak, ciphertext string) (*ent.AccessKey, error) {
	b := tx.AccessKey.Create().SetName(data.GetName()).SetAccessKey(ak).SetSecretCiphertext(ciphertext).SetRoleID(data.GetRoleId()).SetTenantID(tenantID).SetCreatedBy(userID).SetStatus(accesskey.StatusOn).SetCreatedAt(time.Now())
	if data.ExpiresAt != nil {
		b.SetExpiresAt(data.ExpiresAt.AsTime())
	}
	return b.Save(ctx)
}
func (r *AccessKeyRepo) Update(ctx context.Context, req *accesskeyV1.UpdateAccessKeyRequest, tenantID, userID uint32) error {
	tx, err := r.entClient.Client().Tx(ctx)
	if err != nil {
		return keyRepoError(err)
	}
	defer tx.Rollback()
	_, err = tx.AccessKey.Query().Where(accesskey.IDEQ(req.GetKeyId()), accesskey.TenantIDEQ(tenantID)).Only(ctx)
	if err != nil {
		return keyRepoError(err)
	}
	b := tx.AccessKey.UpdateOneID(req.GetKeyId()).SetUpdatedBy(userID).SetUpdatedAt(time.Now())
	for _, path := range req.UpdateMask.Paths {
		switch path {
		case "name":
			b.SetName(req.Data.GetName())
		case "role_id":
			if err = validKeyRole(ctx, tx.Client(), tenantID, req.Data.GetRoleId()); err != nil {
				return err
			}
			b.SetRoleID(req.Data.GetRoleId())
		case "is_active":
			if req.Data.GetIsActive() {
				b.SetStatus(accesskey.StatusOn)
			} else {
				b.SetStatus(accesskey.StatusOff)
			}
		case "expires_at":
			if req.Data.ExpiresAt == nil {
				b.ClearExpiresAt()
			} else {
				b.SetExpiresAt(req.Data.ExpiresAt.AsTime())
			}
		}
	}
	if err = b.Exec(ctx); err != nil {
		return keyRepoError(err)
	}
	if err = tx.Commit(); err != nil {
		return keyRepoError(err)
	}
	return nil
}
func (r *AccessKeyRepo) Delete(ctx context.Context, id, tenantID uint32) error {
	n, err := r.entClient.Client().AccessKey.Delete().Where(accesskey.IDEQ(id), accesskey.TenantIDEQ(tenantID)).Exec(ctx)
	if err != nil {
		return keyRepoError(err)
	}
	if n == 0 {
		return kerrors.NotFound("ACCESS_KEY_NOT_FOUND", "access key not found")
	}
	return nil
}
func (r *AccessKeyRepo) ResetSecret(ctx context.Context, id, tenantID, userID uint32, sk string) (*accesskeyV1.AccessKey, error) {
	ciphertext, err := r.cipher.Encrypt(sk)
	if err != nil {
		return nil, keyRepoError(err)
	}
	tx, err := r.entClient.Client().Tx(ctx)
	if err != nil {
		return nil, keyRepoError(err)
	}
	defer tx.Rollback()
	_, err = tx.AccessKey.Query().Where(accesskey.IDEQ(id), accesskey.TenantIDEQ(tenantID)).Only(ctx)
	if err != nil {
		return nil, keyRepoError(err)
	}
	e, err := tx.AccessKey.UpdateOneID(id).SetSecretCiphertext(ciphertext).SetUpdatedBy(userID).SetUpdatedAt(time.Now()).Save(ctx)
	if err != nil {
		return nil, keyRepoError(err)
	}
	if err = tx.Commit(); err != nil {
		return nil, keyRepoError(err)
	}
	return r.buildAccessKeyDTO(ctx, e)
}

// LookupSigningKey makes only the credential lookup and its bound-role lookup
// without a tenant scope. This local context never escapes into the request and
// never installs a platform/system viewer.
func (r *AccessKeyRepo) LookupSigningKey(ctx context.Context, ak string) (*auth.SigningKey, error) {
	lookupCtx := privacy.DecisionContext(ctx, privacy.Allow)
	e, err := r.entClient.Client().AccessKey.Query().Where(accesskey.AccessKeyEQ(ak)).Only(lookupCtx)
	if ent.IsNotFound(err) {
		return nil, auth.ErrSigningKeyRejected
	}
	if err != nil {
		return nil, fmt.Errorf("lookup signing key: %w", err)
	}
	if e.TenantID == nil || *e.TenantID == 0 || e.Status == nil || *e.Status != accesskey.StatusOn || (e.ExpiresAt != nil && !time.Now().Before(*e.ExpiresAt)) {
		return nil, auth.ErrSigningKeyRejected
	}
	secret, err := r.cipher.Decrypt(e.SecretCiphertext)
	if err != nil {
		return nil, err
	}
	candidate := &auth.SigningKey{ID: e.ID, TenantID: *e.TenantID, Secret: secret}
	bound, err := r.entClient.Client().Role.Query().Where(role.IDEQ(e.RoleID), role.TenantIDEQ(*e.TenantID), role.TypeEQ(role.TypeTenant)).Only(lookupCtx)
	if ent.IsNotFound(err) {
		return candidate, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup signing role: %w", err)
	}
	if bound.Code != nil {
		candidate.Role = *bound.Code
	}
	candidate.RoleAllowed = bound.Code != nil && *bound.Code != "" && bound.Status != nil && *bound.Status == role.StatusOn
	return candidate, nil
}
func (r *AccessKeyRepo) MarkSigningKeyUsed(ctx context.Context, id uint32) error {
	return r.entClient.Client().AccessKey.UpdateOneID(id).SetLastUsedAt(time.Now()).Exec(privacy.DecisionContext(ctx, privacy.Allow))
}
