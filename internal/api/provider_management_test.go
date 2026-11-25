package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/config"
	"github.com/tnqbao/kubereel/internal/database"
	"github.com/tnqbao/kubereel/internal/kubernetes"
	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/service"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	corefake "k8s.io/client-go/kubernetes/fake"
)

func TestProviderManagementReadsRequireAuthAndExcludeSecrets(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	auth := service.NewAuthService(repository.NewAuthRepository(db), config.Defaults().Auth)
	issued, err := auth.Setup(ctx, "admin", "a-strong-password", "", "api")
	if err != nil {
		t.Fatal(err)
	}
	item := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubereel.io/v1alpha1", "kind": "VideoProvider",
		"metadata": map[string]interface{}{"name": "example", "namespace": "test"},
		"spec":     map[string]interface{}{"type": "gemini", "capabilities": []interface{}{"text"}, "models": []interface{}{"example-model"}, "secretRef": map[string]interface{}{"name": "sensitive-secret-name", "key": "sensitive-secret-key"}},
	}}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{kubernetes.VideoProviderResource(): "VideoProviderList"}, item)
	resources := client.Resource(kubernetes.VideoProviderResource()).Namespace("test")
	secrets := corefake.NewSimpleClientset().CoreV1().Secrets("test")
	management := service.NewProviderManagement(repository.NewProviderRepository(resources, secrets))
	server := echo.New()
	(Router{Auth: auth, ProviderManagement: management}).Register(server)
	for _, path := range []string{"/api/v1/providers", "/api/v1/providers/example"} {
		denied := httptest.NewRecorder()
		server.ServeHTTP(denied, httptest.NewRequest(http.MethodGet, path, nil))
		if denied.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", path, denied.Code)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+issued.Token)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "sensitive-secret") || strings.Contains(response.Body.String(), "secretRef") {
			t.Fatalf("secret metadata leaked: %s", response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers?limit=1", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	var page service.ProviderPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || !page.Items[0].CredentialsReferenced {
		t.Fatalf("page = %+v, %v", page, err)
	}
	for _, target := range []struct {
		path   string
		status int
	}{{"/api/v1/providers?limit=0", 400}, {"/api/v1/providers/missing", 404}} {
		req := httptest.NewRequest(http.MethodGet, target.path, nil)
		req.Header.Set("Authorization", "Bearer "+issued.Token)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		if response.Code != target.status {
			t.Fatalf("%s = %d, want %d", target.path, response.Code, target.status)
		}
	}
	call := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1/providers"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+issued.Token)
		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, req)
		if resp.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, resp.Code, resp.Body.String())
		}
		if strings.Contains(resp.Body.String(), "fake-key-") {
			t.Fatal("credential leaked")
		}
		return resp
	}
	call("POST", "", `{"id":"local","type":"mock","capabilities":["image"]}`, 201)
	call("POST", "/local/test", "", 200)
	call("PATCH", "/local", `{"enabled":false}`, 200)
	call("DELETE", "/local", "", 204)
	call("GET", "/local", "", 404)
	csrf := httptest.NewRequest(http.MethodPost, "/api/v1/providers", nil)
	csrf.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: issued.Token})
	denied := httptest.NewRecorder()
	server.ServeHTTP(denied, csrf)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("cookie write without CSRF: %d", denied.Code)
	}

	call("POST", "", `{"id":"external","type":"gemini","capabilities":["text"],"apiKey":"fake-key-one"}`, 201)
	call("POST", "", `{"id":"external","type":"gemini","capabilities":["text"],"apiKey":"fake-key-two"}`, 409)
	call("PATCH", "/external", `{"baseURL":"https://example.com"}`, 400)
	call("PATCH", "/external", `{"secretRef":{"name":"other","key":"key"}}`, 400)
	call("PATCH", "/external", `{"apiKey":"fake-key-two","enabled":false}`, 200)
	stored, err := resources.Get(ctx, "external", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ref, _, err := unstructured.NestedString(stored.Object, "spec", "secretRef", "name")
	if err != nil {
		t.Fatal(err)
	}
	current, err := secrets.Get(ctx, ref, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(current.Data[repository.ProviderAPIKey]) != "fake-key-two" || len(current.OwnerReferences) != 1 || current.OwnerReferences[0].Name != "external" {
		t.Fatal("rotated credential is invalid")
	}
	all, err := secrets.List(ctx, metav1.ListOptions{})
	if err != nil || len(all.Items) != 2 {
		t.Fatalf("expected previous and current credential versions: %v", err)
	}
	call("DELETE", "/external", "", 204)

}
