package argo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"simworkflow/internal/config"
	"simworkflow/internal/connections"
)

type Client struct {
	config config.Argo
	http   *http.Client
}

type Snapshot struct {
	RunNodeNames  map[string]string `json:"-"`
	RunStageNodes map[string][]Node `json:"-"`
	Spec          struct {
		Templates []struct {
			InitContainers []struct {
				Name  string `json:"name"`
				Image string `json:"image"`
				Env   []struct {
					Name string `json:"name"`
				} `json:"env"`
			} `json:"initContainers"`
		} `json:"templates"`
	} `json:"spec"`
	Metadata struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Status struct {
		Phase      string          `json:"phase"`
		Message    string          `json:"message"`
		StartedAt  time.Time       `json:"startedAt"`
		FinishedAt time.Time       `json:"finishedAt"`
		Nodes      map[string]Node `json:"nodes"`
	} `json:"status"`
}

type Node struct {
	Name         string    `json:"name"`
	TemplateName string    `json:"templateName"`
	Type         string    `json:"type"`
	StartedAt    time.Time `json:"startedAt"`
	FinishedAt   time.Time `json:"finishedAt"`
	DisplayName  string    `json:"displayName"`
	Phase        string    `json:"phase"`
	Message      string    `json:"message"`
	PodIP        string    `json:"podIP"`
}

func New(c config.Config) *Client { return &Client{config: c.Argo, http: connections.HTTPClient(c)} }

func (c *Client) call(ctx context.Context, method, suffix string, body any, result any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	u := strings.TrimRight(c.config.Server, "/") + "/api/v1/workflows/" + url.PathEscape(c.config.Namespace) + suffix
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(data))
	if err != nil {
		return errors.New("invalid Argo API request")
	}
	if token := strings.TrimSpace(c.config.Token); token != "" {
		if !strings.HasPrefix(strings.ToLower(token), "bearer ") {
			token = "Bearer " + token
		}
		req.Header.Set("Authorization", token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("Argo request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Argo HTTP %d", resp.StatusCode)
	}
	if result != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(result); err != nil {
			return errors.New("invalid Argo API JSON")
		}
	}
	return nil
}

func (c *Client) Submit(ctx context.Context, workflow any) (*Snapshot, error) {
	var result Snapshot
	err := c.call(ctx, http.MethodPost, "", map[string]any{"workflow": workflow}, &result)
	return &result, err
}

func (c *Client) Get(ctx context.Context, name string) (*Snapshot, error) {
	if name == "" || strings.ContainsAny(name, "/\\?#") {
		return nil, errors.New("invalid workflow name")
	}
	var result Snapshot
	err := c.call(ctx, http.MethodGet, "/"+url.PathEscape(name), nil, &result)
	return &result, err
}
