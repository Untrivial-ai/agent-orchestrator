// Package ecs implements AO sandbox lifecycle against Amazon ECS running on an
// EC2 (Graviton) capacity provider. It mirrors the docker provider's model: the
// worker binary is baked into the task's container image as its entrypoint, so
// the container's main process is the worker itself. The per-session bootstrap
// token, control-plane URL and workspace layout are injected as a RunTask
// container override rather than baked into the task definition.
//
// Unlike NodeOps and Coder there is no idle auto-pause: a task runs until it is
// explicitly terminated. A stopped ECS task is terminal (a task cannot be
// restarted, and its container filesystem does not survive), so Stop is a
// destroy and repair happens through Recreate with a fresh single-use ticket,
// exactly as the docker provider replaces a spent container.
package ecs

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

const (
	tagManaged   = "ao.managed"
	tagProvider  = "ao.provider"
	tagSessionID = "ao.session_id"
	tagOrgID     = "ao.org_id"
	tagNamespace = "ao.ecs.namespace"
)

var (
	environmentKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	namespacePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	// startedByPattern matches the ECS RunTask startedBy constraint. Session ids
	// are UUIDs, which satisfy it; correlation is exact and per-session.
	startedByPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

// api is the subset of the ECS client the provider uses. A test injects a fake;
// the production path uses the concrete *ecs.Client.
type api interface {
	RunTask(context.Context, *ecs.RunTaskInput, ...func(*ecs.Options)) (*ecs.RunTaskOutput, error)
	StopTask(context.Context, *ecs.StopTaskInput, ...func(*ecs.Options)) (*ecs.StopTaskOutput, error)
	DescribeTasks(context.Context, *ecs.DescribeTasksInput, ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
	ListTasks(context.Context, *ecs.ListTasksInput, ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
}

// Config configures one ECS-on-EC2 sandbox provider.
type Config struct {
	// Region is the AWS region the cluster lives in.
	Region string
	// Cluster is the ECS cluster name or ARN that runs sandbox tasks.
	Cluster string
	// TaskDefinition is the family (or family:revision) of the task that bakes
	// the worker image. It must use bridge or host networking so a task needs no
	// per-task ENI; the worker only makes outbound calls to the control plane.
	TaskDefinition string
	// ContainerName is the container in the task definition to inject the
	// per-session bootstrap environment into.
	ContainerName string
	// CapacityProvider, when set, routes RunTask through a capacity provider
	// strategy (an EC2 Auto Scaling group). When empty, RunTask uses the EC2
	// launch type directly.
	CapacityProvider string
	// Namespace scopes ownership tags so a task from another deployment or
	// provider is never adopted or mutated.
	Namespace string

	// API is a test seam. A production client is built from the ambient AWS
	// credentials when it is nil.
	API api
}

// Client manages worker tasks in one ECS cluster and namespace.
type Client struct {
	api              api
	cluster          string
	taskDefinition   string
	containerName    string
	capacityProvider string
	namespace        string
}

var (
	_ sandbox.Provider  = (*Client)(nil)
	_ sandbox.Recreator = (*Client)(nil)
)

// New creates a fail-closed ECS provider. It builds an AWS client from the
// ambient credentials (the control plane's task role) unless a test supplies one.
func New(ctx context.Context, config Config) (*Client, error) {
	cluster := strings.TrimSpace(config.Cluster)
	if cluster == "" {
		return nil, errors.New("ecs: cluster is required")
	}
	taskDefinition := strings.TrimSpace(config.TaskDefinition)
	if taskDefinition == "" {
		return nil, errors.New("ecs: task definition is required")
	}
	containerName := strings.TrimSpace(config.ContainerName)
	if containerName == "" {
		return nil, errors.New("ecs: container name is required")
	}
	namespace := strings.TrimSpace(config.Namespace)
	if !namespacePattern.MatchString(namespace) {
		return nil, fmt.Errorf("ecs: namespace %q is invalid", namespace)
	}
	client := &Client{
		cluster:          cluster,
		taskDefinition:   taskDefinition,
		containerName:    containerName,
		capacityProvider: strings.TrimSpace(config.CapacityProvider),
		namespace:        namespace,
		api:              config.API,
	}
	if client.api == nil {
		region := strings.TrimSpace(config.Region)
		if region == "" {
			return nil, errors.New("ecs: region is required")
		}
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
		if err != nil {
			return nil, fmt.Errorf("ecs: load AWS config: %w", err)
		}
		client.api = ecs.NewFromConfig(awsCfg)
	}
	return client, nil
}

// Create runs one worker task. The container's entrypoint is the baked worker,
// and the per-session bootstrap environment rides as a container override so the
// single-use ticket is never persisted in the task definition.
func (c *Client) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Environment, error) {
	if err := validateIdentity("session id", spec.SessionID); err != nil {
		return sandbox.Environment{}, err
	}
	if err := validateIdentity("organization id", spec.OrgID); err != nil {
		return sandbox.Environment{}, err
	}
	if !startedByPattern.MatchString(spec.SessionID) {
		return sandbox.Environment{}, fmt.Errorf("ecs: session id %q cannot be used as a task correlation key", spec.SessionID)
	}
	overrides, err := containerEnvironment(spec.Environment)
	if err != nil {
		return sandbox.Environment{}, err
	}
	input := &ecs.RunTaskInput{
		Cluster:        aws.String(c.cluster),
		TaskDefinition: aws.String(c.taskDefinition),
		Count:          aws.Int32(1),
		StartedBy:      aws.String(spec.SessionID),
		Tags:           c.ownershipTags(spec),
		Overrides: &ecstypes.TaskOverride{
			ContainerOverrides: []ecstypes.ContainerOverride{{
				Name:        aws.String(c.containerName),
				Environment: overrides,
			}},
		},
	}
	if c.capacityProvider != "" {
		input.CapacityProviderStrategy = []ecstypes.CapacityProviderStrategyItem{{
			CapacityProvider: aws.String(c.capacityProvider),
			Weight:           1,
		}}
	} else {
		input.LaunchType = ecstypes.LaunchTypeEc2
	}
	output, err := c.api.RunTask(ctx, input)
	if err != nil {
		return sandbox.Environment{}, fmt.Errorf("ecs: run task: %w", err)
	}
	if len(output.Tasks) == 0 {
		return sandbox.Environment{}, runTaskFailure(output.Failures)
	}
	task := output.Tasks[0]
	if aws.ToString(task.TaskArn) == "" {
		return sandbox.Environment{}, errors.New("ecs: run task response carried no task ARN")
	}
	return toEnvironment(task), nil
}

// Get describes a worker task and refuses handles outside this provider's
// namespace even if the database points at one.
func (c *Client) Get(ctx context.Context, id sandbox.ID) (sandbox.Environment, error) {
	task, err := c.ownedTask(ctx, id)
	if err != nil {
		return sandbox.Environment{}, err
	}
	return toEnvironment(task), nil
}

// FindBySession adopts a running worker task left behind when the control plane
// crashed between RunTask and the durable observation write. A stopped task is
// terminal in this provider, so it is treated as gone.
func (c *Client) FindBySession(
	ctx context.Context,
	sessionID string,
) (sandbox.Environment, bool, error) {
	if err := validateIdentity("session id", sessionID); err != nil {
		return sandbox.Environment{}, false, err
	}
	if !startedByPattern.MatchString(sessionID) {
		return sandbox.Environment{}, false, nil
	}
	listed, err := c.api.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:       aws.String(c.cluster),
		StartedBy:     aws.String(sessionID),
		DesiredStatus: ecstypes.DesiredStatusRunning,
	})
	if err != nil {
		return sandbox.Environment{}, false, fmt.Errorf("ecs: list tasks: %w", err)
	}
	for _, arn := range listed.TaskArns {
		task, err := c.ownedTask(ctx, sandbox.ID(arn))
		if errors.Is(err, sandbox.ErrNotFound) {
			continue
		}
		if err != nil {
			// A task that is not ours must never be adopted, but a transient
			// describe error should not masquerade as absence.
			if errors.Is(err, errNotOwned) {
				continue
			}
			return sandbox.Environment{}, false, err
		}
		if tagValue(task.Tags, tagSessionID) != sessionID {
			continue
		}
		return toEnvironment(task), true, nil
	}
	return sandbox.Environment{}, false, nil
}

// Start is a readiness check. An ECS task cannot be restarted once stopped, so a
// non-running task returns an error, which makes the reconciler fall back to
// Recreate with a fresh single-use ticket.
func (c *Client) Start(ctx context.Context, id sandbox.ID) error {
	task, err := c.ownedTask(ctx, id)
	if err != nil {
		return err
	}
	if normalizeState(aws.ToString(task.LastStatus)) == sandbox.StateRunning {
		return nil
	}
	return fmt.Errorf("ecs: task %s is not running and cannot be restarted", id)
}

// Stop terminates a worker task. It is destructive: the container filesystem
// does not survive, matching this provider's no-idle-pause contract.
func (c *Client) Stop(ctx context.Context, id sandbox.ID) error {
	if _, err := c.ownedTask(ctx, id); err != nil {
		if errors.Is(err, sandbox.ErrNotFound) {
			return nil
		}
		return err
	}
	return c.stopTask(ctx, id, "ao stop")
}

// Pause aliases Stop. This provider never idle-auto-pauses, and a stopped task
// is terminal, so pausing simply terminates the task.
func (c *Client) Pause(ctx context.Context, id sandbox.ID) error {
	return c.Stop(ctx, id)
}

// Resume aliases Start.
func (c *Client) Resume(ctx context.Context, id sandbox.ID) error {
	return c.Start(ctx, id)
}

// Delete terminates a worker task. It is idempotent: a task that is already gone
// completes silently.
func (c *Client) Delete(ctx context.Context, id sandbox.ID) error {
	_, err := c.ownedTask(ctx, id)
	if errors.Is(err, sandbox.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return c.stopTask(ctx, id, "ao delete")
}

// Recreate replaces compute with a fresh worker launch. The old task is stopped
// and a new one is run with the fresh bootstrap ticket in spec. There is no
// persistent workspace to retain, so this is a clean replacement.
func (c *Client) Recreate(
	ctx context.Context,
	id sandbox.ID,
	spec sandbox.Spec,
) (sandbox.Environment, error) {
	task, err := c.ownedTask(ctx, id)
	if err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		return sandbox.Environment{}, err
	}
	if err == nil {
		if tagValue(task.Tags, tagSessionID) != spec.SessionID {
			return sandbox.Environment{}, errors.New("ecs: refusing to recreate a task for another session")
		}
		if stopErr := c.stopTask(ctx, id, "ao recreate"); stopErr != nil && !errors.Is(stopErr, sandbox.ErrNotFound) {
			return sandbox.Environment{}, stopErr
		}
	}
	return c.Create(ctx, spec)
}

func (c *Client) stopTask(ctx context.Context, id sandbox.ID, reason string) error {
	_, err := c.api.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(c.cluster),
		Task:    aws.String(string(id)),
		Reason:  aws.String(reason),
	})
	if err != nil {
		if isTaskNotFound(err) {
			return nil
		}
		return fmt.Errorf("ecs: stop task %s: %w", id, err)
	}
	return nil
}

// errNotOwned is an internal sentinel: a described task exists but does not carry
// this provider's managed ownership tags.
var errNotOwned = errors.New("ecs: task is not managed by this provider")

func (c *Client) ownedTask(ctx context.Context, id sandbox.ID) (ecstypes.Task, error) {
	if strings.TrimSpace(string(id)) == "" {
		return ecstypes.Task{}, errors.New("ecs: empty task id")
	}
	output, err := c.api.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(c.cluster),
		Tasks:   []string{string(id)},
		Include: []ecstypes.TaskField{ecstypes.TaskFieldTags},
	})
	if err != nil {
		return ecstypes.Task{}, fmt.Errorf("ecs: describe task %s: %w", id, err)
	}
	if len(output.Tasks) == 0 {
		for _, failure := range output.Failures {
			if strings.EqualFold(aws.ToString(failure.Reason), "MISSING") {
				return ecstypes.Task{}, sandbox.ErrNotFound
			}
		}
		return ecstypes.Task{}, sandbox.ErrNotFound
	}
	task := output.Tasks[0]
	if err := c.validateOwnership(task.Tags); err != nil {
		return ecstypes.Task{}, err
	}
	return task, nil
}

func (c *Client) validateOwnership(tags []ecstypes.Tag) error {
	if tagValue(tags, tagManaged) != "true" ||
		tagValue(tags, tagProvider) != sandbox.ProviderECS ||
		tagValue(tags, tagNamespace) != c.namespace ||
		tagValue(tags, tagSessionID) == "" {
		return errNotOwned
	}
	return nil
}

func (c *Client) ownershipTags(spec sandbox.Spec) []ecstypes.Tag {
	tags := map[string]string{
		tagManaged:   "true",
		tagProvider:  sandbox.ProviderECS,
		tagSessionID: spec.SessionID,
		tagOrgID:     spec.OrgID,
		tagNamespace: c.namespace,
	}
	// Spec labels are advisory metadata; the managed keys above always win so an
	// ownership check can never be spoofed through the spec.
	for key, value := range spec.Labels {
		if _, reserved := tags[key]; reserved {
			continue
		}
		tags[key] = value
	}
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]ecstypes.Tag, 0, len(keys))
	for _, key := range keys {
		out = append(out, ecstypes.Tag{Key: aws.String(key), Value: aws.String(tags[key])})
	}
	return out
}

func containerEnvironment(environment map[string]string) ([]ecstypes.KeyValuePair, error) {
	keys := make([]string, 0, len(environment))
	for key, value := range environment {
		if !environmentKeyPattern.MatchString(key) || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("ecs: invalid environment variable %q", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]ecstypes.KeyValuePair, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, ecstypes.KeyValuePair{
			Name:  aws.String(key),
			Value: aws.String(environment[key]),
		})
	}
	return pairs, nil
}

func toEnvironment(task ecstypes.Task) sandbox.Environment {
	return sandbox.Environment{
		ID:       sandbox.ID(aws.ToString(task.TaskArn)),
		Name:     tagValue(task.Tags, tagSessionID),
		State:    normalizeState(aws.ToString(task.LastStatus)),
		Target:   aws.ToString(task.Group),
		Resource: resourceProfile(task),
	}
}

func resourceProfile(task ecstypes.Task) domain.ResourceProfile {
	// Task cpu is in ECS units (1 vCPU == 1024) and memory is in MiB, both as
	// decimal strings. They are best-effort here; a parse miss leaves zero.
	profile := domain.ResourceProfile{}
	if cpu, err := strconv.Atoi(strings.TrimSpace(aws.ToString(task.Cpu))); err == nil && cpu > 0 {
		profile.CPU = cpu / 1024
	}
	if memory, err := strconv.Atoi(strings.TrimSpace(aws.ToString(task.Memory))); err == nil && memory > 0 {
		profile.Memory = memory / 1024
	}
	return profile
}

// normalizeState maps an ECS task lastStatus onto the AO vocabulary. A stopped
// task is terminal in this provider (a task cannot be restarted and its
// filesystem does not survive), so it maps to StateDeleted rather than
// StateStopped: the reconciler then completes a deletion promptly and
// re-provisions a fresh task on resume instead of trying an impossible restart.
func normalizeState(lastStatus string) string {
	switch strings.ToUpper(strings.TrimSpace(lastStatus)) {
	case "RUNNING":
		return sandbox.StateRunning
	case "PROVISIONING", "PENDING", "ACTIVATING":
		return sandbox.StateProvisioning
	case "DEACTIVATING", "STOPPING", "DEPROVISIONING":
		return sandbox.StateDeleting
	case "STOPPED":
		return sandbox.StateDeleted
	default:
		// Anything unrecognized is not-yet-ready, never running, so the startup
		// deadline is preserved.
		return sandbox.StateProvisioning
	}
}

func runTaskFailure(failures []ecstypes.Failure) error {
	if len(failures) == 0 {
		return errors.New("ecs: run task returned no task and no failure")
	}
	messages := make([]string, 0, len(failures))
	atCapacity := false
	for _, failure := range failures {
		reason := strings.TrimSpace(aws.ToString(failure.Reason))
		detail := strings.TrimSpace(aws.ToString(failure.Detail))
		if reasonIsCapacity(reason) {
			atCapacity = true
		}
		message := reason
		if detail != "" {
			message = reason + ": " + detail
		}
		messages = append(messages, message)
	}
	if atCapacity {
		return fmt.Errorf("%w: %s", sandbox.ErrAtCapacity, strings.Join(messages, "; "))
	}
	return fmt.Errorf("ecs: run task failed: %s", strings.Join(messages, "; "))
}

func reasonIsCapacity(reason string) bool {
	upper := strings.ToUpper(reason)
	return strings.HasPrefix(upper, "RESOURCE:") ||
		strings.Contains(upper, "CAPACITY") ||
		strings.Contains(upper, "NO CONTAINER INSTANCES")
}

func isTaskNotFound(err error) bool {
	var invalid *ecstypes.InvalidParameterException
	if errors.As(err, &invalid) {
		return strings.Contains(strings.ToLower(aws.ToString(invalid.Message)), "not found")
	}
	return false
}

func tagValue(tags []ecstypes.Tag, key string) string {
	for _, tag := range tags {
		if aws.ToString(tag.Key) == key {
			return aws.ToString(tag.Value)
		}
	}
	return ""
}

func validateIdentity(name, value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 255 || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("ecs: %s is invalid", name)
	}
	return nil
}
