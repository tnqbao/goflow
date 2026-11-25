package service

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var ErrProviderProbe = errors.New("provider connectivity check failed")
var ErrProviderProbeCredentials = errors.New("provider credentials are missing or were rejected")

type ProviderCheck struct {
	Check  string `json:"check"`
	Status string `json:"status"`
}

var providerProbeClient = &http.Client{
	Timeout:       10 * time.Second,
	Transport:     &http.Transport{DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (s *ProviderManagement) Test(ctx context.Context, id string) (ProviderCheck, error) {
	if s == nil || s.repository == nil {
		return ProviderCheck{}, ErrProviderUnavailable
	}
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return ProviderCheck{}, err
	}
	spec, _, err := unstructured.NestedMap(item.Object, "spec")
	if err != nil {
		return ProviderCheck{}, ErrInvalidProvider
	}
	if err := validateProvider(spec, nil); err != nil {
		return ProviderCheck{}, err
	}
	kind, _ := spec["type"].(string)
	if kind == "mock" {
		return ProviderCheck{Check: "local_configuration", Status: "ok"}, nil
	}
	name, _, _ := unstructured.NestedString(spec, "secretRef", "name")
	key, _, _ := unstructured.NestedString(spec, "secretRef", "key")
	if name == "" || key == "" {
		return ProviderCheck{}, ErrProviderProbeCredentials
	}
	secret, err := s.repository.Credential(ctx, name, key)
	if err != nil || len(secret) == 0 {
		return ProviderCheck{}, ErrProviderProbeCredentials
	}
	models, _, _ := unstructured.NestedStringSlice(spec, "models")
	return probeProvider(ctx, providerProbeClient, kind, string(secret), models)
}

func probeProvider(ctx context.Context, client *http.Client, kind, key string, models []string) (ProviderCheck, error) {
	endpoint := "https://generativelanguage.googleapis.com/v1beta/models?pageSize=1"
	header, value := "x-goog-api-key", key
	check := "model_catalog"
	if kind == "fal" {
		if len(models) == 0 {
			return ProviderCheck{}, invalidProvider("configure a fal model to test pricing access")
		}
		endpoint = "https://api.fal.ai/v1/models/pricing?endpoint_id=" + url.QueryEscape(strings.TrimSpace(models[0]))
		header, value, check = "Authorization", "Key "+key, "model_pricing"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ProviderCheck{}, ErrProviderProbe
	}
	req.Header.Set(header, value)
	resp, err := client.Do(req)
	if err != nil {
		return ProviderCheck{}, ErrProviderProbe
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return ProviderCheck{Check: check, Status: "ok"}, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return ProviderCheck{}, ErrProviderProbeCredentials
	default:
		return ProviderCheck{}, ErrProviderProbe
	}
}
