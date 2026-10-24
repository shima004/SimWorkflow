package argo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"time"
)

const managedLabel = "app.kubernetes.io/managed-by"

// Only these fields are requested. Worker settings and node messages can carry
// signed URLs, and neither belongs in an experiment list or stop response.
const overviewFields = "metadata.name,metadata.namespace,metadata.labels,metadata.creationTimestamp,status.phase,status.startedAt,status.finishedAt,spec.shutdown"

type Overview struct {
	Name       string     `json:"name"`
	Namespace  string     `json:"namespace"`
	Phase      string     `json:"phase"`
	Shutdown   string     `json:"shutdown,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

func (o Overview) Completed() bool {
	return o.Phase == "Succeeded" || o.Phase == "Failed" || o.Phase == "Error"
}

type overviewResource struct {
	Metadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
		CreatedAt time.Time         `json:"creationTimestamp"`
	} `json:"metadata"`
	Status struct {
		Phase      string     `json:"phase"`
		StartedAt  *time.Time `json:"startedAt"`
		FinishedAt *time.Time `json:"finishedAt"`
	} `json:"status"`
	Spec struct {
		Shutdown string `json:"shutdown"`
	} `json:"spec"`
}

func (w overviewResource) overview() Overview {
	phase := w.Status.Phase
	if phase == "" {
		phase = "Pending"
	}
	return Overview{Name: w.Metadata.Name, Namespace: w.Metadata.Namespace, Phase: phase, Shutdown: w.Spec.Shutdown, CreatedAt: w.Metadata.CreatedAt, StartedAt: w.Status.StartedAt, FinishedAt: w.Status.FinishedAt}
}

func (c *Client) List(ctx context.Context) ([]Overview, error) {
	items := make([]Overview, 0)
	continuation := ""
	seen := map[string]bool{}
	for page := 0; page < 10000; page++ {
		var result struct {
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
			Items json.RawMessage `json:"items"`
		}
		fields := "metadata.continue"
		for _, field := range []string{"metadata.name", "metadata.namespace", "metadata.labels", "metadata.creationTimestamp", "status.phase", "status.startedAt", "status.finishedAt", "spec.shutdown"} {
			fields += ",items." + field
		}
		query := url.Values{"listOptions.labelSelector": {managedLabel + "=simworkflow"}, "listOptions.limit": {"100"}, "fields": {fields}}
		if continuation != "" {
			query.Set("listOptions.continue", continuation)
		}
		if err := c.call(ctx, http.MethodGet, "?"+query.Encode(), nil, &result); err != nil {
			return nil, err
		}
		if len(result.Items) == 0 {
			return nil, errors.New("invalid Argo workflow list response")
		}
		var resources []overviewResource
		if err := json.Unmarshal(result.Items, &resources); err != nil {
			return nil, errors.New("invalid Argo workflow list response")
		}
		for _, resource := range resources {
			if resource.Metadata.Labels[managedLabel] == "simworkflow" && resource.Metadata.Namespace == c.config.Namespace {
				items = append(items, resource.overview())
			}
		}
		continuation = result.Metadata.Continue
		if continuation == "" {
			sort.Slice(items, func(i, j int) bool {
				if items[i].CreatedAt.Equal(items[j].CreatedAt) {
					return items[i].Name < items[j].Name
				}
				return items[i].CreatedAt.After(items[j].CreatedAt)
			})
			return items, nil
		}
		if seen[continuation] {
			return nil, errors.New("Argo workflow pagination did not advance")
		}
		seen[continuation] = true
	}
	return nil, errors.New("too many Argo workflow list pages")
}

var workflowName = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)

// Stop requests Argo's Stop shutdown strategy. It does not delete results or
// report that the asynchronous controller has already stopped every Pod.
func (c *Client) Stop(ctx context.Context, name string) (Overview, bool, error) {
	if len(name) > 253 || !workflowName.MatchString(name) {
		return Overview{}, false, errors.New("specify an exact workflow name (aliases and selectors are not supported)")
	}
	var result overviewResource
	query := url.Values{"fields": {overviewFields}}
	if err := c.call(ctx, http.MethodGet, "/"+url.PathEscape(name)+"?"+query.Encode(), nil, &result); err != nil {
		return Overview{}, false, err
	}
	if result.Metadata.Name != name || result.Metadata.Namespace != c.config.Namespace || result.Metadata.Labels[managedLabel] != "simworkflow" {
		return Overview{}, false, errors.New("refusing to stop a workflow that is not managed by SimWorkflow in the configured namespace")
	}
	current := result.overview()
	if current.Completed() || current.Shutdown != "" {
		return current, false, nil
	}
	if err := c.call(ctx, http.MethodPut, "/"+url.PathEscape(name)+"/stop", map[string]string{"namespace": c.config.Namespace, "name": name}, nil); err != nil {
		return Overview{}, false, err
	}
	current.Shutdown = "Stop"
	return current, true, nil
}
