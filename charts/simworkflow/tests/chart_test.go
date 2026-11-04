package charttests

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	"simworkflow/internal/config"
)

func render(t *testing.T, args ...string) []map[string]any {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		if os.Getenv("SIMWORKFLOW_HELM_TEST_REQUIRED") == "1" {
			t.Fatal("Helm is required by this validation job")
		}
		t.Skip("helm is not installed; the Helm CI job runs these checks")
	}
	if _, err := os.Stat("../charts/argo-workflows-2.0.16.tgz"); err != nil {
		if os.Getenv("SIMWORKFLOW_HELM_TEST_REQUIRED") == "1" {
			t.Fatal("Helm dependencies are required by this validation job", err)
		}
		t.Skip("run helm dependency build charts/simworkflow first")
	}
	cmd := exec.Command("helm", append([]string{"template", "lab", "..", "--namespace", "experiments"}, args...)...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, data)
	}
	var documents []map[string]any
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var document map[string]any
		if err := decoder.Decode(&document); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if len(document) > 0 {
			documents = append(documents, document)
		}
	}
	return documents
}

func find(t *testing.T, documents []map[string]any, kind, name string) map[string]any {
	t.Helper()
	for _, document := range documents {
		metadata, _ := document["metadata"].(map[string]any)
		if document["kind"] == kind && metadata["name"] == name {
			return document
		}
	}
	t.Fatalf("missing %s %s", kind, name)
	return nil
}

func section(t *testing.T, document map[string]any, key string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := yaml.Unmarshal([]byte(document["data"].(map[string]any)[key].(string)), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestBundledServicesAndDatabaseWiring(t *testing.T) {
	docs := render(t)
	controller := find(t, docs, "ConfigMap", "lab-argo-workflows-workflow-controller-configmap")
	settings := section(t, controller, "config")
	persistence := settings["persistence"].(map[string]any)
	if persistence["nodeStatusOffLoad"] != true || persistence["archive"] != true {
		t.Fatal("large workflows require offloading and archive", persistence)
	}
	db := persistence["postgresql"].(map[string]any)
	if db["host"] != "lab-postgresql" || db["database"] != "argo" || db["ssl"] != false {
		t.Fatal("database connection differs from bundled PostgreSQL", db)
	}
	spec := settings["workflowDefaults"].(map[string]any)["spec"].(map[string]any)
	gc := spec["podGC"].(map[string]any)
	if gc["strategy"] != "OnPodCompletion" || gc["deleteDelayDuration"] != "10m" || spec["ttlStrategy"] != nil {
		t.Fatal("Pods must be collected without expiring Workflow history", spec)
	}
	for _, component := range []string{"s3", "registry", "postgresql"} {
		find(t, docs, "StatefulSet", "lab-"+component)
		pvc := find(t, docs, "PersistentVolumeClaim", "lab-"+component)
		if pvc["metadata"].(map[string]any)["annotations"].(map[string]any)["helm.sh/resource-policy"] != "keep" {
			t.Fatal("data must survive Helm uninstall", component)
		}
	}
	job := find(t, docs, "Job", "lab-bucket-init-1")
	if job["metadata"].(map[string]any)["annotations"] != nil || job["spec"].(map[string]any)["activeDeadlineSeconds"] != 600 {
		t.Fatal("bucket initialization must be bounded and not a blocking hook", job)
	}
	for _, doc := range docs {
		if doc["kind"] == "Secret" {
			t.Fatal("chart must reference existing credentials, not generate/store credentials")
		}
	}
	cli := find(t, docs, "ConfigMap", "lab-cli-config")
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte(cli["data"].(map[string]any)["config.yaml"].(string)), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := config.ReadYAML(file)
	if err != nil {
		t.Fatal("generated CLI config must satisfy the real CLI parser", err)
	}
	if values["ARGO_WORKFLOW_NAMESPACE"] != "experiments" || values["S3_ENDPOINT"] != "http://lab-s3.experiments.svc:9000" || values["CREDENTIALS_STORE"] != "keyring" {
		t.Fatal("invalid CLI settings", values)
	}
}

func TestExistingServicesDoNotCreateWorkloads(t *testing.T) {
	docs := render(t, "-f", "../examples/existing-services.yaml")
	executor := find(t, docs, "Role", "lab-executor")["rules"].([]any)
	if len(executor) != 1 || executor[0].(map[string]any)["resources"].([]any)[0] != "workflowtaskresults" {
		t.Fatal("executor permissions must remain separate from CLI permissions", executor)
	}
	client := find(t, docs, "Role", "lab-client")["rules"].([]any)
	if len(client) != 3 || client[0].(map[string]any)["resources"].([]any)[0] != "workflows" {
		t.Fatal("CLI permissions must cover experiment operations in this namespace", client)
	}
	for _, doc := range docs {
		switch doc["kind"] {
		case "Deployment", "StatefulSet", "Job", "PersistentVolumeClaim", "CustomResourceDefinition", "Secret":
			t.Fatal("existing-services mode must not deploy infrastructure", doc["kind"])
		}
	}
	cli := section(t, find(t, docs, "ConfigMap", "lab-cli-config"), "config.yaml")
	if cli["argoWorkflow"].(map[string]any)["server"] != "https://argo.example.com" || cli["s3"].(map[string]any)["endpoint"] != "https://s3.example.com" {
		t.Fatal(cli)
	}
}

func TestExternalDatabaseAndDisabledOffloading(t *testing.T) {
	docs := render(t, "-f", "../examples/external-postgresql.yaml", "--set", "postgresql.externalPort=5433")
	settings := section(t, find(t, docs, "ConfigMap", "lab-argo-workflows-workflow-controller-configmap"), "config")
	db := settings["persistence"].(map[string]any)["postgresql"].(map[string]any)
	if db["host"] != "postgres.example.com" || db["port"] != 5433 || db["ssl"] != true || db["passwordSecret"].(map[string]any)["name"] != "argo-database-credentials" {
		t.Fatal(db)
	}
	docs = render(t, "--set", "argo.persistence.enabled=false", "--set", "postgresql.enabled=false")
	settings = section(t, find(t, docs, "ConfigMap", "lab-argo-workflows-workflow-controller-configmap"), "config")
	if settings["persistence"] != nil {
		t.Fatal("persistence explicitly disabled", settings)
	}
}

func TestExistingRBACIsNotAdoptedOrReplaced(t *testing.T) {
	docs := render(t, "-f", "../examples/existing-services.yaml", "--set", "workflow.rbac.create=false", "--set", "workflow.serviceAccount.create=false", "--set", "workflow.clientServiceAccount.create=false")
	if len(docs) != 1 || docs[0]["kind"] != "ConfigMap" {
		t.Fatal("existing infrastructure and RBAC must only generate the non-secret CLI config", docs)
	}
}

func TestIngressStorageAndImagePullSecrets(t *testing.T) {
	docs := render(t, "-f", "../examples/ingress.yaml", "--set", "workflow.serviceAccount.name=custom-worker", "--set", "workflow.imagePullSecrets[0].name=registry-pull", "--set", "s3.persistence.existingClaim=old-maps", "--set-string", "registry.persistence.storageClass=-")
	for _, name := range []string{"lab-s3", "lab-registry", "lab-argo-workflows-server"} {
		find(t, docs, "Ingress", name)
	}
	sa := find(t, docs, "ServiceAccount", "custom-worker")
	if sa["imagePullSecrets"].([]any)[0].(map[string]any)["name"] != "registry-pull" {
		t.Fatal(sa)
	}
	registryPVC := find(t, docs, "PersistentVolumeClaim", "lab-registry")
	if registryPVC["spec"].(map[string]any)["storageClassName"] != "" {
		t.Fatal(registryPVC)
	}
	for _, doc := range docs {
		if doc["kind"] == "PersistentVolumeClaim" && doc["metadata"].(map[string]any)["name"] == "lab-s3" {
			t.Fatal("must not create PVC when existingClaim was selected")
		}
	}
}

func TestMissingExternalEndpointsFailBeforeInstall(t *testing.T) {
	render(t) // Check prerequisites, skipping if Helm/dependencies are absent.
	for _, component := range []string{"argo", "s3", "registry"} {
		cmd := exec.Command("helm", "template", "lab", "..", "--set", component+".enabled=false")
		data, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(data), component+".externalURL is required") {
			t.Fatalf("%s: want actionable missing-endpoint error, got %v %s", component, err, data)
		}
	}
}
