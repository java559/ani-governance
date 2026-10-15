package imagecontract

import (
	"github.com/getkin/kin-openapi/openapi3"
	"go-wind-admin/app/admin/service/cmd/server/assets"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/protobuf/reflect/protoreflect"
	"testing"

	admin "go-wind-admin/api/gen/go/admin/service/v1"
	catalog "go-wind-admin/api/gen/go/catalog/service/v1"
	identity "go-wind-admin/api/gen/go/identity/service/v1"
)

func TestPublicImageSurface(t *testing.T) {
	file := catalog.File_catalog_service_v1_image_proto
	forbidden := map[string]bool{"tenant_id": true, "namespace": true, "role": true, "permissions": true, "management_secret": true, "upstream_url": true, "harbor_project_id": true}
	for i := 0; i < file.Messages().Len(); i++ {
		message := file.Messages().Get(i)
		for j := 0; j < message.Fields().Len(); j++ {
			field := message.Fields().Get(j)
			if forbidden[string(field.Name())] {
				t.Fatalf("public identity/management field %s", field.FullName())
			}
			if field.Name() == "secret" && message.Name() != "IssuePublisherCredentialResponse" && message.Name() != "ResetPublisherCredentialResponse" {
				t.Fatalf("unexpected secret delivery %s", field.FullName())
			}
		}
	}
	services := admin.File_admin_service_v1_i_image_proto.Services()
	if services.Len() != 1 || services.Get(0).Name() != "ImageService" || services.Get(0).Methods().Len() != 11 {
		t.Fatal("unexpected public image service surface")
	}
	for i := 0; i < services.Get(0).Methods().Len(); i++ {
		name := services.Get(0).Methods().Get(i).Name()
		if name == "GetTenantPullMaterial" || name == "ResolveImageForWorkload" {
			t.Fatal("runtime surface exposed")
		}
	}
}

func TestImageModulePreservesExistingContract(t *testing.T) {
	expected := map[string]int32{"MODULE_UNSPECIFIED": 0, "DASHBOARD": 1, "OPM": 2, "SYSTEM": 3, "DICT": 4, "TENANT": 5, "PERMISSION": 6, "LOG": 7, "INTERNAL_MESSAGE": 8, "TASK": 10, "MODEL": 11, "NETWORK": 12, "ACCELERATOR": 13, "IMAGE": 14, "INFERENCE": 15}
	if len(identity.Module_value) != len(expected) {
		t.Fatal("unexpected module additions or removals")
	}
	for name, value := range expected {
		if got, ok := identity.Module_value[name]; !ok || got != value {
			t.Fatalf("module contract changed: %s", name)
		}
	}
}

// gnostic names path variables from JSON names, while Kratos routes retain the
// proto annotation spelling. Catalog/permission lookup requires exact equality.
func TestImageCatalogRouteConsistency(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData(assets.OpenApiData)
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range auth.ImageOperations {
		item := doc.Paths.Value(policy.Path)
		if item == nil {
			t.Fatalf("OpenAPI path differs from HTTP route: %s", policy.Path)
		}
		if item.Operations()[policy.Method] == nil {
			t.Fatalf("OpenAPI method missing: %s %s", policy.Method, policy.Path)
		}
	}
	for _, message := range []string{"ImageRegistration", "GetImageRequest", "UpdateImageRequest", "UnregisterImageRequest"} {
		m := catalog.File_catalog_service_v1_image_proto.Messages().ByName(protoreflect.Name(message))
		if m.Fields().ByName("image_id").JSONName() != "image_id" {
			t.Fatal("Image ID JSON/path spelling drift", message)
		}
	}
}
