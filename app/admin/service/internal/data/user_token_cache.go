package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
)

const (
	// AccessTokenKeyFormat 访问令牌键前缀格式 at:{ct}:{uid}
	AccessTokenKeyFormat = "at:%d:%d"
	// AccessTokenFieldKeyFormat 访问令牌键格式（含 jti）at:{ct}:{uid}:{jti}
	AccessTokenFieldKeyFormat = "at:%d:%d:%s"

	// RefreshTokenKeyFormat 刷新令牌键前缀格式 rt:{ct}:{uid}
	RefreshTokenKeyFormat = "rt:%d:%d"
	// RefreshTokenFieldKeyFormat 刷新令牌键格式（含 jti）rt:{ct}:{uid}:{jti}
	RefreshTokenFieldKeyFormat = "rt:%d:%d:%s"

	// BlacklistKeyFormat 访问令牌黑名单键格式 bl:{jti}
	BlacklistKeyFormat = "bl:%s"

	// UserSessionKeyFormat 用户会话元数据键格式 us:{ct}:{uid}:{jti}
	UserSessionKeyFormat = "us:%d:%d:%s"

	// SessionBindKeyFormat 逻辑会话绑定键格式 sid:{ct}:{uid}:{sid}
	// 值为该逻辑会话当前有效的 jti。它是"稳定 sid → 当前令牌对"的唯一指针，
	// 刷新轮换与登出共用同一原子边界：轮换在此比对旧 jti，登出先删本键，
	// 从而保证登出后任何已准备好的后继刷新都无法提交或复活该 sid。
	SessionBindKeyFormat = "sid:%d:%d:%s"

	// CsrfBindKeyFormat CSRF 证明绑定键格式 csr:{ct}:{uid}:{sid}
	// 值为绑定该逻辑会话的 CSRF token。刷新轮换不更换（sid 不变即证明不变），
	// 登出随会话一并清除。生命周期与 RT 对齐。
	CsrfBindKeyFormat = "csr:%d:%d:%s"

	// scanCount 每次 SCAN 返回的键数量提示（仅是建议值，非强制）
	scanCount = 100
)

// rotateSessionScript 原子轮换逻辑会话：校验 sid 仍指向旧 jti（防重放与双花），
// 删除旧令牌对与会话记录，写入新令牌对、新会话记录，并把 sid 指针更新为新 jti。
// 注意：不触碰 CSRF 证明键——sid 不变即证明不变（bootstrap 不轮换证明，
// 多标签页共享同一证明互不破坏）。
//
// KEYS[1]=sid 绑定键, [2]=旧 AT 键, [3]=旧 RT 键, [4]=旧会话记录键,
// KEYS[5]=新 AT 键, [6]=新 RT 键, [7]=新会话记录键
// ARGV[1]=旧 jti, [2]=新 jti, [3]=新 AT 值, [4]=新 RT 值, [5]=新会话记录 JSON,
// ARGV[6]=AT TTL(ms), [7]=RT TTL(ms), [8]=会话记录 TTL(ms)
// 返回 1=提交成功, 0=sid 不存在或已指向其它 jti（登出/并发轮换），调用方须拒绝。
var rotateSessionScript = redis.NewScript(`
	local sidKey = KEYS[1]
	local cur = redis.call('GET', sidKey)
	if not cur or cur ~= ARGV[1] then
		return 0
	end
	redis.call('DEL', KEYS[2])
	redis.call('DEL', KEYS[3])
	redis.call('DEL', KEYS[4])
	redis.call('SET', KEYS[5], ARGV[3], 'PX', ARGV[6])
	redis.call('SET', KEYS[6], ARGV[4], 'PX', ARGV[7])
	redis.call('SET', KEYS[7], ARGV[5], 'PX', ARGV[8])
	redis.call('SET', sidKey, ARGV[2], 'PX', ARGV[7])
	return 1
`)

// logoutSessionScript 原子退出逻辑会话：以 sid 指针定位当前 jti，
// 先删指针再删令牌对与会话记录，并清除 CSRF 证明。删指针是关键的"先手"：
// 任何已准备好但尚未提交的轮换都会在 sid 比对处失败，无法复活该 sid。
//
// KEYS[1]=sid 绑定键
// ARGV[1]=AT 键前缀（形如 at:{ct}:{uid}:）, [2]=RT 键前缀, [3]=会话记录键前缀,
// ARGV[4]=CSRF 键前缀（形如 csr:{ct}:{uid}:）
// 返回 1=确实撤销了一个活动会话, 0=会话已不存在（幂等）
var logoutSessionScript = redis.NewScript(`
	local sidKey = KEYS[1]
	local cur = redis.call('GET', sidKey)
	if not cur then
		return 0
	end
	redis.call('DEL', sidKey)
	redis.call('DEL', ARGV[1] .. cur)
	redis.call('DEL', ARGV[2] .. cur)
	redis.call('DEL', ARGV[3] .. cur)
	redis.call('DEL', ARGV[4] .. cur)
	return 1
`)

// verifyAndRevokeRefreshTokenScript 原子验证并吊销刷新令牌的 Lua 脚本。
// 在单次 Redis 调用中完成「验证 RT → 删除 RT → 删除 AT → 删除会话元数据」，
// 避免 TOCTOU 竞态。KEYS[3] 为可选的会话元数据键（us:{ct}:{uid}:{jti}），
// 传空串表示不删除。注意：迁移到 String key 后，rtKey/atKey 均为含 jti 的
// 完整 key，使用 GET/DEL 操作。返回值: 1=验证成功, 0=令牌不存在或值不匹配
var verifyAndRevokeRefreshTokenScript = redis.NewScript(`
	local rtKey = KEYS[1]
	local atKey = KEYS[2]
	local usKey = KEYS[3]
	local refreshToken = ARGV[1]

	local stored = redis.call('GET', rtKey)
	if not stored or stored ~= refreshToken then
		return 0
	end

	redis.call('DEL', rtKey)
	redis.call('DEL', atKey)
	if usKey and usKey ~= '' then
		redis.call('DEL', usKey)
	end
	return 1
`)

// UserTokenCache 用户令牌缓存
type UserTokenCache struct {
	log *bLogger.Helper
	rdb *redis.Client
}

func NewUserTokenCache(ctx *bootstrap.Context, rdb *redis.Client) *UserTokenCache {
	utc := &UserTokenCache{
		rdb: rdb,
		log: ctx.NewLoggerHelper("user-token/cache"),
	}
	return utc
}

// AddTokenPair 添加令牌对
func (r *UserTokenCache) AddTokenPair(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	jti string,
	accessToken string,
	refreshToken string,
	accessTokenExpires time.Duration,
	refreshTokenExpires time.Duration,
) error {
	var err error
	pipe := r.rdb.TxPipeline()

	atKey := r.makeAccessTokenFieldKey(clientType, userId, jti)
	pipe.Set(ctx, atKey, accessToken, accessTokenExpires)

	rtKey := r.makeRefreshTokenFieldKey(clientType, userId, jti)
	pipe.Set(ctx, rtKey, refreshToken, refreshTokenExpires)

	_, err = pipe.Exec(ctx)

	return err
}

// AddAccessToken 添加访问令牌
func (r *UserTokenCache) AddAccessToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	jti string,
	accessToken string,
	expires time.Duration,
) error {
	key := r.makeAccessTokenFieldKey(clientType, userId, jti)
	return r.set(ctx, key, accessToken, expires)
}

// AddRefreshToken 添加刷新令牌
func (r *UserTokenCache) AddRefreshToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	jti string,
	refreshToken string,
	expires time.Duration,
) error {
	key := r.makeRefreshTokenFieldKey(clientType, userId, jti)
	return r.set(ctx, key, refreshToken, expires)
}

// AddBlockedAccessToken 添加被阻止的访问令牌
func (r *UserTokenCache) AddBlockedAccessToken(ctx context.Context, jti string, reason string, expires time.Duration) error {
	key := r.makeBlacklistKey(jti)
	return r.set(ctx, key, reason, expires)
}

// GetAccessTokens 获取访问令牌
func (r *UserTokenCache) GetAccessTokens(ctx context.Context, clientType authenticationV1.ClientType, userId uint32) []string {
	pattern := r.makeAccessTokenKey(clientType, userId) + ":*"
	return r.scanValues(ctx, pattern)
}

// GetRefreshTokens 获取刷新令牌
func (r *UserTokenCache) GetRefreshTokens(ctx context.Context, clientType authenticationV1.ClientType, userId uint32) []string {
	pattern := r.makeRefreshTokenKey(clientType, userId) + ":*"
	return r.scanValues(ctx, pattern)
}

// RevokeToken 移除所有令牌（含会话元数据）
func (r *UserTokenCache) RevokeToken(ctx context.Context, clientType authenticationV1.ClientType, userId uint32) error {
	var errs []error
	if err := r.RevokeUserAllAccessToken(ctx, clientType, userId); err != nil {
		r.log.Errorf(ctx, "remove user access token failed: [%v]", err)
		errs = append(errs, err)
	}

	if err := r.RevokeUserAllRefreshToken(ctx, clientType, userId); err != nil {
		r.log.Errorf(ctx, "remove user refresh token failed: [%v]", err)
		errs = append(errs, err)
	}

	// 会话元数据随令牌一并清理，保持「无令牌即无会话记录」的一致性
	if err := r.DeleteUserSessionMetas(ctx, clientType, userId); err != nil {
		r.log.Errorf(ctx, "remove user session metas failed: [%v]", err)
		errs = append(errs, err)
	}

	// 逻辑会话绑定键与 CSRF 证明也一并清理，避免残留 sid 指针指向已删除令牌。
	if err := r.RevokeUserAllSessions(ctx, clientType, userId); err != nil {
		r.log.Errorf(ctx, "remove user session binds failed: [%v]", err)
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

func (r *UserTokenCache) RevokeTokenByJti(ctx context.Context, clientType authenticationV1.ClientType, userId uint32, jti string) error {
	var err error
	if err = r.RevokeAccessToken(ctx, clientType, userId, jti); err != nil {
		r.log.Errorf(ctx, "remove user access token failed: [%v]", err)
	}

	if err = r.RevokeRefreshToken(ctx, clientType, userId, jti); err != nil {
		r.log.Errorf(ctx, "remove user refresh token failed: [%v]", err)
	}

	if err = r.DeleteSessionMeta(ctx, clientType, userId, jti); err != nil {
		r.log.Errorf(ctx, "remove user session meta failed: [%v]", err)
	}

	return err
}

// RevokeAccessToken 移除访问令牌
func (r *UserTokenCache) RevokeAccessToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	jti string,
) error {
	key := r.makeAccessTokenFieldKey(clientType, userId, jti)
	return r.del(ctx, key)
}

// RevokeRefreshToken 移除刷新令牌
func (r *UserTokenCache) RevokeRefreshToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	jti string,
) error {
	key := r.makeRefreshTokenFieldKey(clientType, userId, jti)
	return r.del(ctx, key)
}

// RevokeBlockedAccessToken 撤销被阻止的访问令牌
func (r *UserTokenCache) RevokeBlockedAccessToken(ctx context.Context, jti string) error {
	key := r.makeBlacklistKey(jti)
	return r.del(ctx, key)
}

// IsValidAccessToken 访问令牌是否有效
func (r *UserTokenCache) IsValidAccessToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	jti string,
	uploadedToken string,
) (bool, error) {
	key := r.makeAccessTokenFieldKey(clientType, userId, jti)

	storedToken, err := r.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil // 令牌不存在或已过期
	}

	if storedToken != uploadedToken {
		return false, nil
	}

	return true, nil
}

func (r *UserTokenCache) IsExistAccessTokenByJti(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	jti string,
) (bool, error) {
	key := r.makeAccessTokenFieldKey(clientType, userId, jti)

	_, err := r.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil // 令牌不存在或已过期
	}

	return true, nil
}

// IsExistAccessToken 访问令牌是否存在
func (r *UserTokenCache) IsExistAccessToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	uploadedToken string,
) (exist bool, jti string, err error) {
	pattern := r.makeAccessTokenKey(clientType, userId) + ":*"
	return r.scanFindValue(ctx, pattern, uploadedToken)
}

// IsExistRefreshToken 刷新令牌是否存在
func (r *UserTokenCache) IsExistRefreshToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	uploadedToken string,
) (exist bool, jti string, err error) {
	pattern := r.makeRefreshTokenKey(clientType, userId) + ":*"
	return r.scanFindValue(ctx, pattern, uploadedToken)
}

// IsValidRefreshToken 刷新令牌是否有效
func (r *UserTokenCache) IsValidRefreshToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	jti string,
	uploadedToken string,
) (bool, error) {
	key := r.makeRefreshTokenFieldKey(clientType, userId, jti)

	storedToken, err := r.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil // 令牌不存在或已过期
	}

	if storedToken != uploadedToken {
		return false, nil
	}

	return true, nil
}

// IsBlockedAccessToken 访问令牌是否被阻止
func (r *UserTokenCache) IsBlockedAccessToken(ctx context.Context, jti string) bool {
	key := r.makeBlacklistKey(jti)
	n, err := r.rdb.Exists(ctx, key).Result()
	if err != nil {
		// This boolean is an authorization decision: inability to check the
		// blacklist must deny access, not establish that the token is unblocked.
		r.log.Error(ctx, "access token blacklist lookup failed")
		return true
	}
	return n > 0
}

// RevokeUserAllAccessToken 删除访问令牌
func (r *UserTokenCache) RevokeUserAllAccessToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
) error {
	pattern := r.makeAccessTokenKey(clientType, userId) + ":*"
	return r.delByPattern(ctx, pattern)
}

// RevokeUserAllRefreshToken 删除刷新令牌
func (r *UserTokenCache) RevokeUserAllRefreshToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
) error {
	pattern := r.makeRefreshTokenKey(clientType, userId) + ":*"
	return r.delByPattern(ctx, pattern)
}

// RevokeUserAllSessions 删除某用户的全部逻辑会话绑定键与 CSRF 证明键。
// 全端踢下线（改密等）时调用，避免残留 sid 指针指向已删除的令牌对。
func (r *UserTokenCache) RevokeUserAllSessions(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
) error {
	var errs []error
	if err := r.delByPattern(ctx, fmt.Sprintf(SessionBindKeyFormat, clientType.Number(), userId, "*")); err != nil {
		errs = append(errs, err)
	}
	if err := r.delByPattern(ctx, fmt.Sprintf(CsrfBindKeyFormat, clientType.Number(), userId, "*")); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// VerifyAndRevokeTokenPair 原子验证并吊销刷新令牌及其关联的访问令牌。
// 使用 Lua 脚本保证「验证→删除」操作的原子性，避免 TOCTOU 竞态条件。
// 返回 (true, nil) 表示刷新令牌有效且已成功吊销旧令牌对。
//
// 说明：新刷新链已改用 RotateSession（先准备后提交）。本方法保留给
// 既有远程测试与兼容调用；生产刷新路径不再使用它（它会在验证通过时
// 立即消费旧会话，不符合"准备失败不消费"的合同）。
func (r *UserTokenCache) VerifyAndRevokeTokenPair(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	jti string,
	refreshToken string,
) (bool, error) {
	rtKey := r.makeRefreshTokenFieldKey(clientType, userId, jti)
	atKey := r.makeAccessTokenFieldKey(clientType, userId, jti)
	// 会话元数据随旧令牌对一并原子删除（刷新轮换后旧会话记录即失效）
	usKey := r.makeUserSessionKey(clientType, userId, jti)

	result, err := verifyAndRevokeRefreshTokenScript.Run(ctx, r.rdb, []string{rtKey, atKey, usKey}, refreshToken).Int64()
	if err != nil {
		r.log.Errorf(ctx, "verifyAndRevokeTokenPair failed for user [%d] jti [%s]: %v", userId, jti, err)
		return false, err
	}

	return result == 1, nil
}

// BindSessionAndStore 写入令牌对并把 sid 指针绑定到该 jti（登录路径，无旧会话）。
// 会话元数据由调用方随后经 SaveSessionMeta 写入（保持既有展示链不变）。
func (r *UserTokenCache) BindSessionAndStore(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	sid string,
	jti string,
	accessToken string,
	refreshToken string,
	accessTokenExpires time.Duration,
	refreshTokenExpires time.Duration,
) error {
	if sid == "" || jti == "" {
		return fmt.Errorf("bind session: sid and jti are required")
	}

	pipe := r.rdb.TxPipeline()
	pipe.Set(ctx, r.makeAccessTokenFieldKey(clientType, userId, jti), accessToken, accessTokenExpires)
	pipe.Set(ctx, r.makeRefreshTokenFieldKey(clientType, userId, jti), refreshToken, refreshTokenExpires)
	// sid 指针 TTL 与 RT 对齐：RT 过期即会话失效，指针无需单独清理。
	pipe.Set(ctx, r.makeSessionBindKey(clientType, userId, sid), jti, refreshTokenExpires)

	_, err := pipe.Exec(ctx)
	if err != nil {
		r.log.Errorf(ctx, "bind session for user [%d] sid [%s] failed: %v", userId, sid, err)
	}
	return err
}

// RotateSession 原子轮换逻辑会话（刷新路径的"提交"阶段）。
// 校验 sid 仍指向 oldJti，删除旧令牌对/旧会话记录，写入新令牌对/新会话记录，
// 并把 sid 指针更新为 newJti。返回 false 表示 sid 已失效（登出/并发），须拒绝。
func (r *UserTokenCache) RotateSession(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	sid string,
	oldJti string,
	newJti string,
	accessToken string,
	refreshToken string,
	newMeta *SessionMeta,
	accessTokenExpires time.Duration,
	refreshTokenExpires time.Duration,
) (bool, error) {
	if sid == "" || oldJti == "" || newJti == "" {
		return false, fmt.Errorf("rotate session: sid, oldJti and newJti are required")
	}

	metaJSON := ""
	if newMeta != nil {
		raw, mErr := json.Marshal(newMeta)
		if mErr != nil {
			return false, fmt.Errorf("rotate session: marshal session meta failed: %w", mErr)
		}
		metaJSON = string(raw)
	}

	sidKey := r.makeSessionBindKey(clientType, userId, sid)
	oldAtKey := r.makeAccessTokenFieldKey(clientType, userId, oldJti)
	oldRtKey := r.makeRefreshTokenFieldKey(clientType, userId, oldJti)
	oldUsKey := r.makeUserSessionKey(clientType, userId, oldJti)
	newAtKey := r.makeAccessTokenFieldKey(clientType, userId, newJti)
	newRtKey := r.makeRefreshTokenFieldKey(clientType, userId, newJti)
	newUsKey := r.makeUserSessionKey(clientType, userId, newJti)

	res, err := rotateSessionScript.Run(
		ctx,
		r.rdb,
		[]string{sidKey, oldAtKey, oldRtKey, oldUsKey, newAtKey, newRtKey, newUsKey},
		oldJti,
		newJti,
		accessToken,
		refreshToken,
		metaJSON,
		accessTokenExpires.Milliseconds(),
		refreshTokenExpires.Milliseconds(),
		refreshTokenExpires.Milliseconds(),
	).Int64()
	if err != nil {
		r.log.Errorf(ctx, "rotate session failed for user [%d] sid [%s]: %v", userId, sid, err)
		return false, err
	}
	return res == 1, nil
}

// RevokeSession 退出逻辑会话：以 sid 指针定位当前 jti，删指针与令牌对/会话记录。
// 先删指针是关键：任何已准备好但未提交的轮换会在 sid 比对处失败，无法复活该 sid。
func (r *UserTokenCache) RevokeSession(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	sid string,
) (bool, error) {
	if sid == "" {
		return false, nil
	}
	sidKey := r.makeSessionBindKey(clientType, userId, sid)
	atPrefix := r.makeAccessTokenKey(clientType, userId) + ":"
	rtPrefix := r.makeRefreshTokenKey(clientType, userId) + ":"
	usPrefix := fmt.Sprintf(UserSessionKeyFormat, clientType.Number(), userId, "")
	csrfPrefix := fmt.Sprintf(CsrfBindKeyFormat, clientType.Number(), userId, "")

	res, err := logoutSessionScript.Run(ctx, r.rdb, []string{sidKey}, atPrefix, rtPrefix, usPrefix, csrfPrefix).Int64()
	if err != nil {
		r.log.Errorf(ctx, "revoke session failed for user [%d] sid [%s]: %v", userId, sid, err)
		return false, err
	}
	return res == 1, nil
}

// SetCsrfToken 为逻辑会话写入 CSRF 证明（SetNX，已存在则不覆盖）。
// bootstrap 不轮换证明：多次调用返回同一证明，避免多标签页互相破坏。
// 会话不存在（sid 指针缺失，如已登出）时拒绝写入，防止为已退出会话写孤儿证明。
// 返回最终生效的证明值（可能是既有的），err 表示 Redis 故障。
func (r *UserTokenCache) SetCsrfToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	sid string,
	token string,
	expires time.Duration,
) (string, error) {
	if sid == "" || token == "" {
		return "", fmt.Errorf("set csrf token: sid and token are required")
	}
	exists, err := r.rdb.Exists(ctx, r.makeSessionBindKey(clientType, userId, sid)).Result()
	if err != nil {
		r.log.Errorf(ctx, "check session bind for csrf failed for user [%d] sid [%s]: %v", userId, sid, err)
		return "", err
	}
	if exists == 0 {
		return "", fmt.Errorf("set csrf token: session not found")
	}

	key := r.makeCsrfKey(clientType, userId, sid)
	// SetNX：证明已存在（bootstrap 重复调用）时保留原值，不轮换。
	ok, err := r.rdb.SetNX(ctx, key, token, expires).Result()
	if err != nil {
		r.log.Errorf(ctx, "set csrf token failed for user [%d] sid [%s]: %v", userId, sid, err)
		return "", err
	}
	if !ok {
		// 已存在：读取既有证明返回，保证同一 sid 多次 bootstrap 得到同一值。
		return r.getCsrfToken(ctx, clientType, userId, sid)
	}
	return token, nil
}

// getCsrfToken 读取会话的 CSRF 证明；会话无证明返回空串（不视为错误）。
func (r *UserTokenCache) getCsrfToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	sid string,
) (string, error) {
	key := r.makeCsrfKey(clientType, userId, sid)
	val, err := r.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		r.log.Errorf(ctx, "get csrf token failed for user [%d] sid [%s]: %v", userId, sid, err)
		return "", err
	}
	return val, nil
}

// GetCsrfToken 读取会话的 CSRF 证明（导出）；会话无证明返回空串。
func (r *UserTokenCache) GetCsrfToken(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	sid string,
) (string, error) {
	return r.getCsrfToken(ctx, clientType, userId, sid)
}

// ==============================
// key 生成
// ==============================

// makeAccessTokenKey 生成访问令牌键前缀 at:{ct}:{uid}（用于 SCAN 匹配）
func (r *UserTokenCache) makeAccessTokenKey(clientType authenticationV1.ClientType, userId uint32) string {
	return fmt.Sprintf(AccessTokenKeyFormat, clientType.Number(), userId)
}

// makeSessionBindKey 生成逻辑会话绑定键 sid:{ct}:{uid}:{sid}
func (r *UserTokenCache) makeSessionBindKey(clientType authenticationV1.ClientType, userId uint32, sid string) string {
	return fmt.Sprintf(SessionBindKeyFormat, clientType.Number(), userId, sid)
}

// makeCsrfKey 生成 CSRF 证明绑定键 csr:{ct}:{uid}:{sid}
func (r *UserTokenCache) makeCsrfKey(clientType authenticationV1.ClientType, userId uint32, sid string) string {
	return fmt.Sprintf(CsrfBindKeyFormat, clientType.Number(), userId, sid)
}

// makeAccessTokenFieldKey 生成访问令牌键（含 jti）at:{ct}:{uid}:{jti}
func (r *UserTokenCache) makeAccessTokenFieldKey(clientType authenticationV1.ClientType, userId uint32, jti string) string {
	return fmt.Sprintf(AccessTokenFieldKeyFormat, clientType.Number(), userId, jti)
}

// makeRefreshTokenKey 生成刷新令牌键前缀 rt:{ct}:{uid}（用于 SCAN 匹配）
func (r *UserTokenCache) makeRefreshTokenKey(clientType authenticationV1.ClientType, userId uint32) string {
	return fmt.Sprintf(RefreshTokenKeyFormat, clientType.Number(), userId)
}

// makeRefreshTokenFieldKey 生成刷新令牌键（含 jti）rt:{ct}:{uid}:{jti}
func (r *UserTokenCache) makeRefreshTokenFieldKey(clientType authenticationV1.ClientType, userId uint32, jti string) string {
	return fmt.Sprintf(RefreshTokenFieldKeyFormat, clientType.Number(), userId, jti)
}

// makeBlacklistKey 生成黑名单键
func (r *UserTokenCache) makeBlacklistKey(jti string) string {
	return fmt.Sprintf(BlacklistKeyFormat, jti)
}

// ==============================
// 基础 String 操作（黑名单等使用）
// ==============================

func (r *UserTokenCache) set(ctx context.Context, key string, value string, expires time.Duration) error {
	if err := r.rdb.Set(ctx, key, value, expires).Err(); err != nil {
		r.log.Errorf(ctx, "set key[%s] value[%s] failed: %v", key, value, err)
		return err
	}
	return nil
}

// del 删除键
func (r *UserTokenCache) del(ctx context.Context, key string) error {
	if err := r.rdb.Del(ctx, key).Err(); err != nil {
		r.log.Errorf(ctx, "del key[%s] failed: %v", key, err)
		return err
	}
	return nil
}

// ==============================
// 基于 SCAN 的批量操作（替代原 Hash 操作）
// ==============================

// scanKeys 使用 SCAN 遍历匹配 pattern 的所有 key。
// 正确处理游标迭代，直到游标归零为止，避免遗漏。
func (r *UserTokenCache) scanKeys(ctx context.Context, pattern string) ([]string, error) {
	var keys []string
	var cursor uint64
	var err error

	for {
		var batch []string
		batch, cursor, err = r.rdb.Scan(ctx, cursor, pattern, scanCount).Result()
		if err != nil {
			r.log.Errorf(ctx, "scan pattern[%s] failed: %v", pattern, err)
			return nil, err
		}

		keys = append(keys, batch...)

		if cursor == 0 {
			break
		}
	}

	return keys, nil
}

// scanValues 获取匹配 pattern 的所有 key 的值。
func (r *UserTokenCache) scanValues(ctx context.Context, pattern string) []string {
	keys, err := r.scanKeys(ctx, pattern)
	if err != nil || len(keys) == 0 {
		return []string{}
	}

	values, err := r.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		r.log.Errorf(ctx, "mget pattern[%s] failed: %v", pattern, err)
		return []string{}
	}

	var result []string
	for _, v := range values {
		if v == nil {
			continue // key 已过期
		}
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}

	return result
}

// scanFindValue 在匹配 pattern 的所有 key 中查找值等于 target 的 key，并返回其 jti。
// 用于「按 token 值反查 jti」的场景（如黑名单操作）。
func (r *UserTokenCache) scanFindValue(ctx context.Context, pattern string, target string) (bool, string, error) {
	keys, err := r.scanKeys(ctx, pattern)
	if err != nil {
		return false, "", err
	}
	if len(keys) == 0 {
		return false, "", nil
	}

	values, err := r.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		r.log.Errorf(ctx, "mget pattern[%s] failed: %v", pattern, err)
		return false, "", err
	}

	for i, v := range values {
		if s, ok := v.(string); ok && s == target {
			return true, r.extractJtiFromKey(keys[i]), nil
		}
	}

	return false, "", nil
}

// delByPattern 删除匹配 pattern 的所有 key。
// 使用 SCAN + pipeline DEL，避免阻塞 Redis（不使用 KEYS）。
func (r *UserTokenCache) delByPattern(ctx context.Context, pattern string) error {
	keys, err := r.scanKeys(ctx, pattern)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}

	if err := r.rdb.Del(ctx, keys...).Err(); err != nil {
		r.log.Errorf(ctx, "del by pattern[%s] failed: %v", pattern, err)
		return err
	}

	return nil
}

// extractJtiFromKey 从键名 at:{ct}:{uid}:{jti} 中提取末段的 jti。
func (r *UserTokenCache) extractJtiFromKey(key string) string {
	parts := strings.Split(key, ":")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// ==============================
// 用户会话元数据（在线用户功能）
// ==============================

// SessionMeta 会话元数据：令牌签发时记录的客户端信息，
// 供在线会话列表展示。生命周期与 refresh token 对齐（TTL 同步设置），
// 过期或吊销即从 Redis 消失，无需额外清理。
type SessionMeta struct {
	Username  string `json:"username,omitempty"`
	TenantId  uint32 `json:"tenantId,omitempty"`
	Ip        string `json:"ip,omitempty"`
	UserAgent string `json:"ua,omitempty"`
	DeviceId  string `json:"dev,omitempty"`
	// LoginAt 登录时间（unix 秒）。刷新轮换继承首次登录时间，不重置。
	LoginAt int64 `json:"loginAt"`
}

// SessionEntry 一次会话扫描得到的完整条目（键定位信息 + 元数据）。
type SessionEntry struct {
	ClientType authenticationV1.ClientType
	UserId     uint32
	Jti        string
	Meta       *SessionMeta
}

// SaveSessionMeta 记录会话元数据，TTL 与 refresh token 一致。
func (r *UserTokenCache) SaveSessionMeta(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	jti string,
	meta *SessionMeta,
	expires time.Duration,
) error {
	if jti == "" || meta == nil {
		return nil
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		r.log.Errorf(ctx, "marshal session meta failed for user [%d]: %v", userId, err)
		return err
	}
	key := r.makeUserSessionKey(clientType, userId, jti)
	return r.set(ctx, key, string(raw), expires)
}

// GetSessionMeta 读取单个会话元数据；会话不存在（已过期/被吊销）返回 nil。
func (r *UserTokenCache) GetSessionMeta(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	jti string,
) (*SessionMeta, error) {
	key := r.makeUserSessionKey(clientType, userId, jti)
	raw, err := r.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		r.log.Errorf(ctx, "get session meta failed for user [%d]: %v", userId, err)
		return nil, err
	}
	return decodeSessionMeta(raw)
}

// DeleteSessionMeta 删除会话元数据（令牌吊销/轮换后清理）。
func (r *UserTokenCache) DeleteSessionMeta(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
	jti string,
) error {
	if jti == "" {
		return nil
	}
	key := r.makeUserSessionKey(clientType, userId, jti)
	return r.del(ctx, key)
}

// DeleteUserSessionMetas 删除某用户全部会话元数据（全端踢下线后清理）。
func (r *UserTokenCache) DeleteUserSessionMetas(
	ctx context.Context,
	clientType authenticationV1.ClientType,
	userId uint32,
) error {
	pattern := fmt.Sprintf(UserSessionKeyFormat, clientType.Number(), userId, "*")
	return r.delByPattern(ctx, pattern)
}

// ListSessionEntries 扫描全部会话元数据。
// 按 us:{ct}:{uid}:{jti} 键名拆出定位信息，MGET 取值，
// 已过期键在 MGET 中返回 nil 自动跳过，无需额外 TTL 判断。
func (r *UserTokenCache) ListSessionEntries(ctx context.Context) ([]SessionEntry, error) {
	keys, err := r.scanKeys(ctx, "us:*")
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return []SessionEntry{}, nil
	}

	values, err := r.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		r.log.Errorf(ctx, "mget session metas failed: %v", err)
		return nil, err
	}

	entries := make([]SessionEntry, 0, len(keys))
	for i, v := range values {
		s, ok := v.(string)
		if !ok {
			continue // key 已过期
		}
		meta, err := decodeSessionMeta(s)
		if err != nil || meta == nil {
			r.log.Warnf(ctx, "decode session meta failed for key [%s]: %v", keys[i], err)
			continue
		}
		entry, ok := parseUserSessionKey(keys[i])
		if !ok {
			continue
		}
		entry.Meta = meta
		entries = append(entries, *entry)
	}

	return entries, nil
}

// makeUserSessionKey 生成会话元数据键 us:{ct}:{uid}:{jti}
func (r *UserTokenCache) makeUserSessionKey(clientType authenticationV1.ClientType, userId uint32, jti string) string {
	return fmt.Sprintf(UserSessionKeyFormat, clientType.Number(), userId, jti)
}

// parseUserSessionKey 从键名 us:{ct}:{uid}:{jti} 拆出定位信息。
func parseUserSessionKey(key string) (*SessionEntry, bool) {
	parts := strings.SplitN(key, ":", 4)
	if len(parts) != 4 || parts[3] == "" {
		return nil, false
	}
	ct, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, false
	}
	uid, err := strconv.Atoi(parts[2])
	if err != nil {
		return nil, false
	}
	return &SessionEntry{
		ClientType: authenticationV1.ClientType(ct),
		UserId:     uint32(uid),
		Jti:        parts[3],
	}, true
}

func decodeSessionMeta(raw string) (*SessionMeta, error) {
	var meta SessionMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}
