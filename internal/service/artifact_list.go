package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/tnqbao/kubereel/internal/workflow"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const artifactRunPageSize = 20
const MaxArtifactCursor = 8192

type ArtifactPage struct {
	Items    []Artifact `json:"items"`
	Continue string     `json:"continue,omitempty"`
}

type artifactCursor struct {
	RunID  string `json:"r,omitempty"`
	Token  string `json:"t,omitempty"`
	Offset int    `json:"o,omitempty"`
}

type listedArtifact struct {
	runID string
	ref   artifactReference
}

func (s *Artifacts) List(ctx context.Context, runID string, limit int, cursor string) (ArtifactPage, error) {
	if s == nil || s.runs == nil || s.store == nil {
		return ArtifactPage{}, ErrStorageUnavailable
	}
	if limit < 1 || limit > 100 {
		return ArtifactPage{}, workflow.ErrInvalid
	}
	pageCursor, err := decodeArtifactCursor(runID, cursor)
	if err != nil {
		return ArtifactPage{}, err
	}
	runs, next, err := s.artifactRuns(ctx, pageCursor)
	if err != nil {
		return ArtifactPage{}, err
	}

	var refs []listedArtifact
	for i := range runs {
		for _, ref := range runArtifactReferences(&runs[i]) {
			refs = append(refs, listedArtifact{runID: runs[i].GetName(), ref: ref})
		}
	}
	if pageCursor.Offset > len(refs) {
		return ArtifactPage{}, workflow.ErrInvalid
	}
	page := ArtifactPage{Items: make([]Artifact, 0, limit)}
	offset := pageCursor.Offset
	for offset < len(refs) && len(page.Items) < limit {
		item := refs[offset]
		offset++
		file, err := s.open(ctx, item.runID, item.ref)
		if errors.Is(err, ErrArtifactNotFound) {
			continue
		}
		if err != nil {
			return ArtifactPage{}, err
		}
		if err := file.Object.Close(); err != nil {
			return ArtifactPage{}, err
		}
		page.Items = append(page.Items, file.Artifact)
	}
	if offset < len(refs) {
		pageCursor.Offset = offset
		page.Continue, err = encodeArtifactCursor(pageCursor)
		return page, err
	}
	if next != "" {
		page.Continue, err = encodeArtifactCursor(artifactCursor{Token: next})
	}
	return page, err
}

func (s *Artifacts) artifactRuns(ctx context.Context, cursor artifactCursor) ([]unstructured.Unstructured, string, error) {
	if cursor.RunID != "" {
		run, err := s.getRun(ctx, cursor.RunID)
		if err != nil {
			return nil, "", err
		}
		return []unstructured.Unstructured{*run}, "", nil
	}
	page, err := s.runs.List(ctx, artifactRunPageSize, cursor.Token)
	if err != nil {
		return nil, "", err
	}
	return page.Items, page.GetContinue(), nil
}

func decodeArtifactCursor(runID, value string) (artifactCursor, error) {
	if runID != "" && validateWorkflowID(runID) != nil {
		return artifactCursor{}, workflow.ErrInvalid
	}
	if value == "" {
		return artifactCursor{RunID: runID}, nil
	}
	if len(value) > MaxArtifactCursor {
		return artifactCursor{}, workflow.ErrInvalid
	}
	encoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return artifactCursor{}, workflow.ErrInvalid
	}
	var c artifactCursor
	if err := json.Unmarshal(encoded, &c); err != nil {
		return artifactCursor{}, workflow.ErrInvalid
	}
	if c.RunID != runID || c.Offset < 0 || (runID != "" && c.Token != "") {
		return artifactCursor{}, workflow.ErrInvalid
	}
	return c, nil
}

func encodeArtifactCursor(c artifactCursor) (string, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
