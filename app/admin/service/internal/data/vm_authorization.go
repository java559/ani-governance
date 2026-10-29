package data

import (
	"context"
	identity "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
)

const VMCollectionPath = "/api/v1/virtual-machines"
const VMGetPath = "/api/v1/virtual-machines/{vm_id}"
const VMDeletePath = "/api/v1/virtual-machines/{data.vm_id}"
const VMOperationPath = "/api/v1/virtual-machines/{vm_id}/operations/{operation_id}"
const VMMediaPath = "/api/v1/virtual-machine-media"
const VMDNSDefaultsPath = "/api/v1/virtual-machine-dns-defaults"
const VMLogsPath = "/api/v1/virtual-machines/{vm_id}/logs"

type VMRoute struct{ Path, Method string }

var vmRoutes = map[string]VMRoute{
	"create": {VMCollectionPath, "POST"}, "list": {VMCollectionPath, "GET"}, "get": {VMGetPath, "GET"}, "delete": {VMDeletePath, "DELETE"}, "operation": {VMOperationPath, "GET"},
	"start":            {"/api/v1/virtual-machines/{data.vm_id}/start", "POST"},
	"stop":             {"/api/v1/virtual-machines/{data.vm_id}/stop", "POST"},
	"restart":          {"/api/v1/virtual-machines/{data.vm_id}/restart", "POST"},
	"force-stop":       {"/api/v1/virtual-machines/{data.vm_id}/force-stop", "POST"},
	"console":          {"/api/v1/virtual-machines/{data.vm_id}/console-sessions", "POST"},
	"logs":             {VMLogsPath, "GET"},
	"disks":            {"/api/v1/virtual-machines/{vm_id}/disks", "GET"},
	"create-disk":      {"/api/v1/virtual-machines/{data.vm_id}/disks", "POST"},
	"attach-disk":      {"/api/v1/virtual-machines/{data.vm_id}/disks/{data.disk_id}/attach", "POST"},
	"detach-disk":      {"/api/v1/virtual-machines/{data.vm_id}/disks/{data.disk_id}/detach", "POST"},
	"delete-disk":      {"/api/v1/virtual-machines/{data.vm_id}/disks/{data.disk_id}", "DELETE"},
	"media":            {VMMediaPath, "GET"},
	"dns-defaults":     {VMDNSDefaultsPath, "GET"},
	"set-media":        {"/api/v1/virtual-machines/{data.vm_id}/media/{data.slot}", "PUT"},
	"eject-media":      {"/api/v1/virtual-machines/{data.vm_id}/media/{data.slot}", "DELETE"},
	"set-boot":         {"/api/v1/virtual-machines/{data.vm_id}/boot", "PUT"},
	"snapshots":        {"/api/v1/virtual-machines/{vm_id}/snapshots", "GET"},
	"snapshot":         {"/api/v1/virtual-machines/{vm_id}/snapshots/{snapshot_id}", "GET"},
	"create-snapshot":  {"/api/v1/virtual-machines/{data.vm_id}/snapshots", "POST"},
	"delete-snapshot":  {"/api/v1/virtual-machines/{data.vm_id}/snapshots/{data.snapshot_id}", "DELETE"},
	"preview-restore":  {"/api/v1/virtual-machines/{data.vm_id}/snapshots/{data.snapshot_id}/restore-preview", "POST"},
	"restore-snapshot": {"/api/v1/virtual-machines/{data.vm_id}/snapshots/{data.snapshot_id}/restore", "POST"},
}

func VMActionRoute(action string) (VMRoute, bool) { r, ok := vmRoutes[action]; return r, ok }
func VMModuleRegistered() bool {
	id, ok := identity.Module_value["VIRTUAL_MACHINE"]
	return ok && id == 17
}

type VMAuthorizationRepo struct{ current *ActionAuthorizationRepo }

func NewVMAuthorizationRepo(c *entCrud.EntClient[*ent.Client]) *VMAuthorizationRepo {
	return &VMAuthorizationRepo{NewActionAuthorizationRepo(c)}
}
func (r *VMAuthorizationRepo) Authorize(ctx context.Context, tenant, user uint32, path, method string) error {
	if r == nil || r.current == nil || !VMModuleRegistered() {
		return ErrActionAuthorizationDenied
	}
	allowed := false
	for _, route := range vmRoutes {
		if route.Path == path && route.Method == method {
			allowed = true
			break
		}
	}
	if !allowed {
		return ErrActionAuthorizationDenied
	}
	return r.current.authorizeRoute(ctx, tenant, user, path, "VIRTUAL_MACHINE", method)
}
