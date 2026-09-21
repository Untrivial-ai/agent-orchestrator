package ecs

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

// fakeAPI records the last input for each ECS call and returns canned output.
type fakeAPI struct {
	runInput      *ecs.RunTaskInput
	runOutput     *ecs.RunTaskOutput
	runErr        error
	stopInput     *ecs.StopTaskInput
	stopErr       error
	describeInput *ecs.DescribeTasksInput
	describeOut   *ecs.DescribeTasksOutput
	describeErr   error
	listInput     *ecs.ListTasksInput
	listOut       *ecs.ListTasksOutput
	listErr       error
}

func (f *fakeAPI) RunTask(_ context.Context, in *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	f.runInput = in
	return f.runOutput, f.runErr
}

func (f *fakeAPI) StopTask(_ context.Context, in *ecs.StopTaskInput, _ ...func(*ecs.Options)) (*ecs.StopTaskOutput, error) {
	f.stopInput = in
	return &ecs.StopTaskOutput{}, f.stopErr
}

func (f *fakeAPI) DescribeTasks(_ context.Context, in *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	f.describeInput = in
	return f.describeOut, f.describeErr
}

func (f *fakeAPI) ListTasks(_ context.Context, in *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	f.listInput = in
	return f.listOut, f.listErr
}

func newTestClient(t *testing.T, fake *fakeAPI) *Client {
	t.Helper()
	client, err := New(context.Background(), Config{
		Cluster:        "ao-sandboxes",
		TaskDefinition: "ao-worker",
		ContainerName:  "worker",
		Namespace:      "ao-cloud",
		API:            fake,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func ownedTags(sessionID string) []ecstypes.Tag {
	return []ecstypes.Tag{
		{Key: aws.String(tagManaged), Value: aws.String("true")},
		{Key: aws.String(tagProvider), Value: aws.String(sandbox.ProviderECS)},
		{Key: aws.String(tagNamespace), Value: aws.String("ao-cloud")},
		{Key: aws.String(tagSessionID), Value: aws.String(sessionID)},
	}
}

func TestNewValidation(t *testing.T) {
	cases := map[string]Config{
		"missing cluster":        {TaskDefinition: "td", ContainerName: "c", Namespace: "ns", API: &fakeAPI{}},
		"missing task def":       {Cluster: "cl", ContainerName: "c", Namespace: "ns", API: &fakeAPI{}},
		"missing container name": {Cluster: "cl", TaskDefinition: "td", Namespace: "ns", API: &fakeAPI{}},
		"bad namespace":          {Cluster: "cl", TaskDefinition: "td", ContainerName: "c", Namespace: "bad namespace!", API: &fakeAPI{}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(context.Background(), cfg); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestCreateRunsTaskWithOverridesAndTags(t *testing.T) {
	fake := &fakeAPI{
		runOutput: &ecs.RunTaskOutput{
			Tasks: []ecstypes.Task{{
				TaskArn:    aws.String("arn:aws:ecs:eu-north-1:1:task/ao/abc"),
				LastStatus: aws.String("PROVISIONING"),
				Tags:       ownedTags("11111111-1111-1111-1111-111111111111"),
			}},
		},
	}
	client := newTestClient(t, fake)
	env, err := client.Create(context.Background(), sandbox.Spec{
		SessionID: "11111111-1111-1111-1111-111111111111",
		OrgID:     "org-1",
		Environment: map[string]string{
			"AO_CLOUD_SESSION_ID":       "11111111-1111-1111-1111-111111111111",
			"AO_WORKER_BOOTSTRAP_TOKEN": "ticket-xyz",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if env.State != sandbox.StateProvisioning {
		t.Fatalf("state = %q, want provisioning", env.State)
	}
	if string(env.ID) != "arn:aws:ecs:eu-north-1:1:task/ao/abc" {
		t.Fatalf("id = %q", env.ID)
	}
	in := fake.runInput
	if aws.ToString(in.Cluster) != "ao-sandboxes" || aws.ToString(in.TaskDefinition) != "ao-worker" {
		t.Fatalf("cluster/taskdef wrong: %q/%q", aws.ToString(in.Cluster), aws.ToString(in.TaskDefinition))
	}
	if aws.ToString(in.StartedBy) != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("startedBy = %q", aws.ToString(in.StartedBy))
	}
	if in.LaunchType != ecstypes.LaunchTypeEc2 {
		t.Fatalf("launch type = %q, want EC2", in.LaunchType)
	}
	if len(in.Overrides.ContainerOverrides) != 1 ||
		aws.ToString(in.Overrides.ContainerOverrides[0].Name) != "worker" {
		t.Fatalf("container override target wrong: %+v", in.Overrides.ContainerOverrides)
	}
	env0 := in.Overrides.ContainerOverrides[0].Environment
	if len(env0) != 2 || aws.ToString(env0[0].Name) != "AO_CLOUD_SESSION_ID" {
		t.Fatalf("env override not sorted/complete: %+v", env0)
	}
	if tagValue(in.Tags, tagManaged) != "true" ||
		tagValue(in.Tags, tagProvider) != sandbox.ProviderECS ||
		tagValue(in.Tags, tagSessionID) != "11111111-1111-1111-1111-111111111111" ||
		tagValue(in.Tags, tagNamespace) != "ao-cloud" {
		t.Fatalf("ownership tags wrong: %+v", in.Tags)
	}
}

func TestCreateUsesCapacityProviderWhenSet(t *testing.T) {
	fake := &fakeAPI{
		runOutput: &ecs.RunTaskOutput{Tasks: []ecstypes.Task{{
			TaskArn: aws.String("arn:task/x"), LastStatus: aws.String("PENDING"),
			Tags: ownedTags("22222222-2222-2222-2222-222222222222"),
		}}},
	}
	client, err := New(context.Background(), Config{
		Cluster: "cl", TaskDefinition: "td", ContainerName: "worker",
		CapacityProvider: "ao-graviton", Namespace: "ao-cloud", API: fake,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Create(context.Background(), sandbox.Spec{
		SessionID: "22222222-2222-2222-2222-222222222222", OrgID: "org",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if fake.runInput.LaunchType != "" {
		t.Fatalf("launch type should be empty when capacity provider set, got %q", fake.runInput.LaunchType)
	}
	if len(fake.runInput.CapacityProviderStrategy) != 1 ||
		aws.ToString(fake.runInput.CapacityProviderStrategy[0].CapacityProvider) != "ao-graviton" {
		t.Fatalf("capacity provider strategy wrong: %+v", fake.runInput.CapacityProviderStrategy)
	}
}

func TestCreateAtCapacity(t *testing.T) {
	fake := &fakeAPI{
		runOutput: &ecs.RunTaskOutput{
			Failures: []ecstypes.Failure{{
				Reason: aws.String("RESOURCE:MEMORY"),
				Detail: aws.String("no instance had enough memory"),
			}},
		},
	}
	client := newTestClient(t, fake)
	_, err := client.Create(context.Background(), sandbox.Spec{
		SessionID: "33333333-3333-3333-3333-333333333333", OrgID: "org",
	})
	if !errors.Is(err, sandbox.ErrAtCapacity) {
		t.Fatalf("err = %v, want ErrAtCapacity", err)
	}
}

func TestGetOwnershipAndState(t *testing.T) {
	sessionID := "44444444-4444-4444-4444-444444444444"
	fake := &fakeAPI{
		describeOut: &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
			TaskArn: aws.String("arn:task/y"), LastStatus: aws.String("RUNNING"),
			Tags: ownedTags(sessionID),
		}}},
	}
	client := newTestClient(t, fake)
	env, err := client.Get(context.Background(), "arn:task/y")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if env.State != sandbox.StateRunning {
		t.Fatalf("state = %q, want running", env.State)
	}
	// The describe must request tags for the ownership check.
	if len(fake.describeInput.Include) != 1 || fake.describeInput.Include[0] != ecstypes.TaskFieldTags {
		t.Fatalf("describe must include TAGS, got %+v", fake.describeInput.Include)
	}
}

func TestGetRejectsForeignTask(t *testing.T) {
	fake := &fakeAPI{
		describeOut: &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
			TaskArn:    aws.String("arn:task/z"),
			LastStatus: aws.String("RUNNING"),
			Tags:       []ecstypes.Tag{{Key: aws.String(tagManaged), Value: aws.String("true")}},
		}}},
	}
	client := newTestClient(t, fake)
	if _, err := client.Get(context.Background(), "arn:task/z"); !errors.Is(err, errNotOwned) {
		t.Fatalf("err = %v, want errNotOwned", err)
	}
}

func TestGetMissingIsNotFound(t *testing.T) {
	fake := &fakeAPI{
		describeOut: &ecs.DescribeTasksOutput{
			Failures: []ecstypes.Failure{{Arn: aws.String("arn:task/gone"), Reason: aws.String("MISSING")}},
		},
	}
	client := newTestClient(t, fake)
	if _, err := client.Get(context.Background(), "arn:task/gone"); !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestNormalizeState(t *testing.T) {
	cases := map[string]string{
		"RUNNING":        sandbox.StateRunning,
		"PROVISIONING":   sandbox.StateProvisioning,
		"PENDING":        sandbox.StateProvisioning,
		"ACTIVATING":     sandbox.StateProvisioning,
		"DEACTIVATING":   sandbox.StateDeleting,
		"STOPPING":       sandbox.StateDeleting,
		"DEPROVISIONING": sandbox.StateDeleting,
		"STOPPED":        sandbox.StateDeleted,
		"WAT":            sandbox.StateProvisioning,
	}
	for status, want := range cases {
		if got := normalizeState(status); got != want {
			t.Errorf("normalizeState(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestStartRunningNoopStoppedErrors(t *testing.T) {
	sessionID := "55555555-5555-5555-5555-555555555555"
	running := &fakeAPI{describeOut: &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
		TaskArn: aws.String("arn:task/r"), LastStatus: aws.String("RUNNING"), Tags: ownedTags(sessionID),
	}}}}
	if err := newTestClient(t, running).Start(context.Background(), "arn:task/r"); err != nil {
		t.Fatalf("Start(running) = %v, want nil", err)
	}
	stopped := &fakeAPI{describeOut: &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
		TaskArn: aws.String("arn:task/s"), LastStatus: aws.String("STOPPED"), Tags: ownedTags(sessionID),
	}}}}
	if err := newTestClient(t, stopped).Start(context.Background(), "arn:task/s"); err == nil {
		t.Fatal("Start(stopped) = nil, want error so the reconciler recreates")
	}
}

func TestDeleteStopsTaskAndIsIdempotent(t *testing.T) {
	sessionID := "66666666-6666-6666-6666-666666666666"
	fake := &fakeAPI{describeOut: &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
		TaskArn: aws.String("arn:task/d"), LastStatus: aws.String("RUNNING"), Tags: ownedTags(sessionID),
	}}}}
	client := newTestClient(t, fake)
	if err := client.Delete(context.Background(), "arn:task/d"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if fake.stopInput == nil || aws.ToString(fake.stopInput.Task) != "arn:task/d" {
		t.Fatalf("StopTask not called on delete: %+v", fake.stopInput)
	}
	// A task that is already gone deletes silently.
	missing := &fakeAPI{describeOut: &ecs.DescribeTasksOutput{
		Failures: []ecstypes.Failure{{Reason: aws.String("MISSING")}},
	}}
	if err := newTestClient(t, missing).Delete(context.Background(), "arn:task/gone"); err != nil {
		t.Fatalf("Delete(missing) = %v, want nil", err)
	}
	if missing.stopInput != nil {
		t.Fatal("StopTask must not be called for a missing task")
	}
}

func TestFindBySessionAdoptsRunningTask(t *testing.T) {
	sessionID := "77777777-7777-7777-7777-777777777777"
	fake := &fakeAPI{
		listOut: &ecs.ListTasksOutput{TaskArns: []string{"arn:task/found"}},
		describeOut: &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
			TaskArn: aws.String("arn:task/found"), LastStatus: aws.String("RUNNING"), Tags: ownedTags(sessionID),
		}}},
	}
	client := newTestClient(t, fake)
	env, found, err := client.FindBySession(context.Background(), sessionID)
	if err != nil || !found {
		t.Fatalf("FindBySession found=%v err=%v", found, err)
	}
	if string(env.ID) != "arn:task/found" {
		t.Fatalf("id = %q", env.ID)
	}
	if aws.ToString(fake.listInput.StartedBy) != sessionID ||
		fake.listInput.DesiredStatus != ecstypes.DesiredStatusRunning {
		t.Fatalf("list filters wrong: startedBy=%q desired=%q",
			aws.ToString(fake.listInput.StartedBy), fake.listInput.DesiredStatus)
	}
}

func TestFindBySessionNoneFound(t *testing.T) {
	fake := &fakeAPI{listOut: &ecs.ListTasksOutput{}}
	client := newTestClient(t, fake)
	_, found, err := client.FindBySession(context.Background(), "88888888-8888-8888-8888-888888888888")
	if err != nil || found {
		t.Fatalf("FindBySession found=%v err=%v, want false/nil", found, err)
	}
}

func TestRecreateRejectsForeignSession(t *testing.T) {
	fake := &fakeAPI{describeOut: &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
		TaskArn: aws.String("arn:task/old"), LastStatus: aws.String("RUNNING"),
		Tags: ownedTags("99999999-9999-9999-9999-999999999999"),
	}}}}
	client := newTestClient(t, fake)
	_, err := client.Recreate(context.Background(), "arn:task/old", sandbox.Spec{
		SessionID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", OrgID: "org",
	})
	if err == nil {
		t.Fatal("Recreate across sessions must fail")
	}
	if fake.stopInput != nil {
		t.Fatal("must not stop another session's task")
	}
}

func TestRecreateStopsOldAndRunsNew(t *testing.T) {
	sessionID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	fake := &fakeAPI{
		describeOut: &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
			TaskArn: aws.String("arn:task/old"), LastStatus: aws.String("RUNNING"), Tags: ownedTags(sessionID),
		}}},
		runOutput: &ecs.RunTaskOutput{Tasks: []ecstypes.Task{{
			TaskArn: aws.String("arn:task/new"), LastStatus: aws.String("PROVISIONING"), Tags: ownedTags(sessionID),
		}}},
	}
	client := newTestClient(t, fake)
	env, err := client.Recreate(context.Background(), "arn:task/old", sandbox.Spec{
		SessionID: sessionID, OrgID: "org",
	})
	if err != nil {
		t.Fatalf("Recreate: %v", err)
	}
	if fake.stopInput == nil || aws.ToString(fake.stopInput.Task) != "arn:task/old" {
		t.Fatalf("old task not stopped: %+v", fake.stopInput)
	}
	if string(env.ID) != "arn:task/new" {
		t.Fatalf("new id = %q", env.ID)
	}
}
