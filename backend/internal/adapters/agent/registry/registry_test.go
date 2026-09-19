package registry

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestGetAgentHooksFootprintIsGitignored enforces a contract every shipped
// (and future) adapter must hold: any file GetAgentHooks writes into a session
// worktree must be covered by a sibling AO-managed self-ignoring .gitignore
// (hookutil.EnsureWorkspaceGitignore). Hook files are untracked, and
// `git worktree remove` (without --force) refuses on any untracked file — an
// uncovered hook file makes every one of that adapter's session workspaces
// permanently undeletable (kill/cleanup can never free them).
func TestGetAgentHooksFootprintIsGitignored(t *testing.T) {
	for _, ha := range Harnessed() {
		t.Run(string(ha.Harness), func(t *testing.T) {
			ws := t.TempDir()
			if ha.Harness == "autohand" {
				t.Setenv("AUTOHAND_CONFIG", filepath.Join(t.TempDir(), "config.json"))
			}
			cfg := ports.WorkspaceHookConfig{
				SessionID:     "proj-1",
				WorkspacePath: ws,
				DataDir:       t.TempDir(),
			}
			if ha.Harness == "kimi" {
				cfg.Env = map[string]string{"KIMI_CODE_HOME": filepath.Join(cfg.DataDir, "kimi")}
			}
			ensureAgentBinary(t, string(ha.Harness))
			if err := ha.Agent.GetAgentHooks(context.Background(), cfg); err != nil {
				t.Fatalf("GetAgentHooks: %v", err)
			}
			files := workspaceFiles(t, ws)
			for _, rel := range files {
				gitignorePath := filepath.Join(ws, filepath.Dir(rel), ".gitignore")
				data, err := os.ReadFile(gitignorePath) //nolint:gosec // test-owned temp dir
				if err != nil {
					t.Errorf("hook file %q has no sibling .gitignore (%v); it will keep the session worktree permanently dirty", rel, err)
					continue
				}
				content := string(data)
				if !strings.Contains(content, hookutil.GitignoreSentinel) {
					t.Errorf(".gitignore next to %q is not AO-managed (missing sentinel)", rel)
					continue
				}
				if entry := "/" + filepath.Base(rel); !hasLine(content, entry) {
					t.Errorf(".gitignore next to %q does not list %q", rel, entry)
				}
			}
		})
	}
}

func TestEveryHarnessReportsAuthStatus(t *testing.T) {
	for _, ha := range Harnessed() {
		if _, ok := ha.Agent.(ports.AgentAuthChecker); !ok {
			t.Errorf("%s does not implement ports.AgentAuthChecker", ha.Harness)
		}
	}
}

func TestEveryInScopeHarnessAuthStatusIsIsolated(t *testing.T) {
	tests := map[domain.AgentHarness]struct {
		binary      string
		environment string
		localEnv    map[string]string
		check       ports.AgentAuthCheck
	}{
		"agy": {binary: "agy", environment: "GEMINI_API_KEY", localEnv: map[string]string{"GEMINI_API_KEY": "fixture-key"}},
		"aider": {binary: "aider", environment: `AIDER_ENV_FILE AIDER_SET_ENV AIDER_API_KEY AIDER_MODEL AIDER_OPENAI_API_KEY AIDER_ANTHROPIC_API_KEY AIDER_OPENAI_API_BASE OPENAI_API_BASE
OPENAI_API_KEY ANTHROPIC_API_KEY OPENROUTER_API_KEY DEEPSEEK_API_KEY GEMINI_API_KEY GOOGLE_API_KEY VERTEXAI_PROJECT VERTEXAI_LOCATION AZURE_API_BASE AZURE_OPENAI_ENDPOINT AZURE_API_VERSION AZURE_OPENAI_API_VERSION AZURE_AI_API_BASE AZURE_API_KEY AZURE_OPENAI_API_KEY AZURE_AI_API_KEY OLLAMA_API_KEY LM_STUDIO_API_KEY GITHUB_COPILOT_TOKEN ALEPH_ALPHA_API_KEY ALEPHALPHA_API_KEY ANYSCALE_API_KEY BASETEN_API_KEY BYTEZ_API_KEY CEREBRAS_API_KEY CLARIFAI_API_KEY CLOUDFLARE_API_KEY CODESTRAL_API_KEY CO_API_KEY COHERE_API_KEY COMPACTIFAI_API_KEY DASHSCOPE_API_KEY DATABRICKS_API_KEY DEEPINFRA_API_KEY FEATHERLESS_AI_API_KEY FIREWORKS_AI_API_KEY FIREWORKS_API_KEY FIREWORKSAI_API_KEY GROQ_API_KEY HUGGINGFACE_API_KEY INFINITY_API_KEY MARITALK_API_KEY MISTRAL_API_KEY MOONSHOT_API_KEY NEBIUS_API_KEY NLP_CLOUD_API_KEY NOVITA_API_KEY NVIDIA_NIM_API_KEY OPENAI_LIKE_API_KEY OR_API_KEY OVHCLOUD_API_KEY PALM_API_KEY PERPLEXITYAI_API_KEY PREDIBASE_API_KEY PROVIDER_API_KEY REPLICATE_API_KEY SAMBANOVA_API_KEY TOGETHERAI_API_KEY USER_API_KEY VERCEL_AI_GATEWAY_API_KEY ARK_API_KEY VOLCENGINE_API_KEY VOYAGE_API_KEY WANDB_API_KEY WATSONX_API_KEY WX_API_KEY XAI_API_KEY XINFERENCE_API_KEY`, localEnv: map[string]string{"OPENAI_API_KEY": "fixture-key"}, check: ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "openai/gpt-5"}}},
		"amp":      {binary: "amp", environment: "AMP_API_KEY AMP_SETTINGS_FILE AMP_URL", localEnv: map[string]string{"AMP_API_KEY": "sgamp_fixture"}},
		"auggie":   {binary: "auggie", environment: "AUGMENT_SESSION_AUTH", localEnv: map[string]string{"AUGMENT_SESSION_AUTH": `{"accessToken":"fixture","tenantURL":"https://example.invalid","scopes":["email"]}`}},
		"autohand": {binary: "autohand", environment: `AUTOHAND_CONFIG AUTOHAND_HOME AUTOHAND_PROVIDER AUTOHAND_CODE_SIMPLE AUTOHAND_API_KEY AUTOHAND_AI_API_KEY AUTOHAND_AI_BASE_URL AUTOHAND_AI_PLAN AUTOHAND_MODEL AWS_PROFILE AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AZURE_OPENAI_ENDPOINT AZURE_OPENAI_DEPLOYMENT AZURE_OPENAI_KEY AZURE_TENANT_ID AZURE_CLIENT_ID AZURE_CLIENT_SECRET OPENROUTER_API_KEY OPENAI_API_KEY LLM_GATEWAY_API_KEY ZAI_API_KEY SAKANA_API_KEY DEEPSEEK_API_KEY GEMINI_API_KEY GOOGLE_API_KEY GROQ_API_KEY MISTRAL_API_KEY ANTHROPIC_API_KEY IDENTITY_ENDPOINT IDENTITY_HEADER MSI_ENDPOINT MSI_SECRET`, localEnv: map[string]string{"AUTOHAND_AI_API_KEY": "fixture-key"}},
		"cline": {binary: "cline", environment: `CLINE_API_KEY CLINE_PROVIDER_SETTINGS_PATH CLINE_DATA_DIR CLINE_DIR AI_API_URL
AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_PROFILE AWS_DEFAULT_PROFILE AWS_CONFIG_FILE AWS_SHARED_CREDENTIALS_FILE AWS_WEB_IDENTITY_TOKEN_FILE AWS_ROLE_ARN AWS_BEARER_TOKEN_BEDROCK AWS_REGION AWS_DEFAULT_REGION
GOOGLE_APPLICATION_CREDENTIALS CLOUDSDK_CONFIG GOOGLE_VERTEX_PROJECT GOOGLE_VERTEX_LOCATION GOOGLE_CLOUD_PROJECT GCLOUD_PROJECT GOOGLE_CLOUD_LOCATION VERTEXAI_PROJECT VERTEXAI_LOCATION
AZURE_OPENAI_API_KEY AZURE_API_KEY AZURE_TENANT_ID AZURE_CLIENT_ID AZURE_CLIENT_SECRET AZURE_FEDERATED_TOKEN_FILE IDENTITY_ENDPOINT IDENTITY_HEADER MSI_ENDPOINT MSI_SECRET
AICORE_SERVICE_KEY VCAP_SERVICES
ABACUS_API_KEY ABLIT_KEY ABOVE_API_KEY AGENTROUTER_API_KEY AGNES_API_KEY AIAND_API_KEY AIHUBMIX_API_KEY AIXY_API_KEY AI_GATEWAY_API_KEY AI_ROUTER_API_KEY AKI_IO_API_KEY ALIBABA_CODING_PLAN_API_KEY ALIBABA_TOKEN_PLAN_API_KEY AMBIENT_API_KEY AMD_API_KEY ANTHROPIC_API_KEY ANYAPI_API_KEY ARCEE_API_KEY ARK_API_KEY ARK_CODING_PLAN_API_KEY ASKSAGE_API_KEY ATOMIC_CHAT_API_KEY AURIKO_API_KEY BAILING_API_TOKEN BASETEN_API_KEY BERGET_API_KEY BLUECLAW_API_KEY BOTHUB_API_KEY CEREBRAS_API_KEY CHUTES_API_KEY CLARIFAI_PAT CLAUDINIO_API_KEY CLOUDFERRO_SHERLOCK_API_KEY CLOUDFLARE_API_KEY CORAL_API_KEY CORTECS_API_KEY CROF_API_KEY CROSSMODEL_API_KEY CRUSOE_API_KEY DAOXE_API_KEY DASHSCOPE_API_KEY DATABRICKS_TOKEN DEEPSEEK_API_KEY DIFY_API_KEY DIGITALOCEAN_ACCESS_TOKEN DINFERENCE_API_KEY DOUBAO_API_KEY DRUN_API_KEY EBCLOUD_API_KEY ECHO_API_KEY EDENAI_API_KEY ELEVENLABS_API_KEY EMPIRIOLABS_API_KEY EVROC_API_KEY FASTROUTER_API_KEY FIREWORKS_API_KEY FREEMODEL_API_KEY FRIENDLI_TOKEN FROGBOT_API_KEY GEMINI_API_KEY GITHUB_TOKEN GMICLOUD_API_KEY GOOGLE_GENERATIVE_AI_API_KEY GREENPT_API_KEY GROQ_API_KEY HELICONE_API_KEY HETZNER_API_KEY HF_TOKEN HICAP_API_KEY HPC_AI_API_KEY HUAWEI_CLOUD_MAAS_API_KEY HYPER_API_KEY IFLOW_API_KEY IMPOSSIBL_API_KEY INCEPTION_API_KEY INCEPTRON_API_KEY INFERENCE_API_KEY INFERX_API_KEY INFER_API_KEY INFOMANIAK_API_KEY IOINTELLIGENCE_API_KEY ITERACOMPUTE_API_KEY JALAPENO_API_KEY JIEKOU_API_KEY KENARI_API_KEY KILO_GATEWAY_API_KEY KIMI_API_KEY KLOKINTEGRATION_API_KEY KOSMIK_API_KEY KUAE_API_KEY LILAC_API_KEY LITELLM_API_KEY LLAMA_API_KEY LLMGATEWAY_API_KEY LLMTECH_API_KEY LLMTR_API_KEY LMSTUDIO_API_KEY LONGCAT_API_KEY LUCIDQUERY_API_KEY LYNKR_API_KEY MEGANOVA_API_KEY MELIOUS_API_KEY META_MODEL_API_KEY MINIMAX_API_KEY MISTRAL_API_KEY MIXLAYER_API_KEY MOARK_API_KEY MODAL_PROXY_TOKEN MODELIS_API_KEY MODELSCOPE_API_KEY MODEL_ORACLE_API_KEY MOONSHOT_API_KEY MORPH_API_KEY NANO_GPT_API_KEY NAN_API_KEY NEARAI_API_KEY NEBIUS_API_KEY NEON_AI_GATEWAY_TOKEN NEOSMITH_API_KEY NEURALWATT_API_KEY NOUSRESEARCH_API_KEY NOUS_RESEARCH_API_KEY NOVA_API_KEY NOVITA_API_KEY NVIDIA_API_KEY OCA_API_KEY OFOX_API_KEY OLLAMA_API_KEY OPENAI_API_KEY OPENCODE_API_KEY OPENREASON_API_KEY OPENROUTER_API_KEY OPPER_API_KEY ORCAROUTER_API_KEY OVHCLOUD_API_KEY PENDRA_API_KEY PERPLEXITY_API_KEY PIONEER_API_KEY POE_API_KEY POOLSIDE_API_KEY PRIVATEMODE_API_KEY QIHANG_API_KEY QINIU_API_KEY QWEN_API_KEY REGOLO_API_KEY REQUESTY_API_KEY ROUTING_RUN_API_KEY RUNINFRA_GATEWAY_KEY SAKANA_API_KEY SAMBANOVA_API_KEY SARVAM_API_KEY SCALEWAY_API_KEY SCNET_API_KEY SCX_API_KEY SENSENOVA_API_KEY SILICONFLOW_API_KEY SILICONFLOW_CN_API_KEY SNOWFLAKE_CORTEX_PAT STACKIT_API_KEY STANDARDCOMPUTE_API_KEY STEPFUN_API_KEY SUBCONSCIOUS_API_KEY SUBMODEL_INSTAGEN_ACCESS_KEY SYNTHETIC_API_KEY TENCENT_CODING_PLAN_API_KEY TENCENT_TOKENHUB_API_KEY TENCENT_TOKEN_PLAN_API_KEY TENSORX_API_KEY THEGRID_API_KEY TINFOIL_API_KEY TINKER_API_KEY TOGETHER_API_KEY TOKENGO_API_KEY TOKENROUTER_API_KEY TRUSTEDROUTER_API_KEY UMANS_AI_API_KEY UMANS_AI_CODING_PLAN_API_KEY UNOROUTER_API_KEY UPSTAGE_API_KEY V0_API_KEY VANCINE_API_KEY VCAP_SERVICES VIVGRID_API_KEY VULTR_API_KEY WAFER_API_KEY WALLABY_API_KEY WANDB_API_KEY XAI_API_KEY XIAOMI_API_KEY XPERSONA_API_KEY ZELDOC_API_KEY ZENIFRA_AI_KEY ZENMUX_API_KEY ZHIPU_API_KEY`, localEnv: map[string]string{"CLINE_API_KEY": "fixture-key"}},
		"continue": {binary: "cn", environment: "CONTINUE_API_KEY CONTINUE_GLOBAL_DIR ANTHROPIC_API_KEY", localEnv: map[string]string{"CONTINUE_API_KEY": "fixture-key"}},
		"copilot":  {binary: "copilot", environment: "COPILOT_GITHUB_TOKEN GH_TOKEN GITHUB_TOKEN COPILOT_HOME COPILOT_MODEL COPILOT_PROVIDER_BASE_URL COPILOT_PROVIDER_TYPE COPILOT_PROVIDER_API_KEY COPILOT_PROVIDER_BEARER_TOKEN GH_CONFIG_DIR GH_HOST", localEnv: map[string]string{"COPILOT_GITHUB_TOKEN": "fixture-token"}},
		"crush":    {binary: "crush", environment: "HYPER_API_KEY ANTHROPIC_API_KEY OPENAI_API_KEY VERCEL_API_KEY GEMINI_API_KEY ZAI_API_KEY MINIMAX_API_KEY SYNTHETIC_API_KEY HF_TOKEN CEREBRAS_API_KEY OPENROUTER_API_KEY IONET_API_KEY ALIBABA_SINGAPORE_API_KEY ALIBABA_US_API_KEY GROQ_API_KEY AVIAN_API_KEY OPENCODE_API_KEY AZURE_OPENAI_API_KEY MOONSHOT_API_KEY VERTEXAI_PROJECT VERTEXAI_LOCATION CRUSH_GLOBAL_CONFIG CRUSH_GLOBAL_DATA", localEnv: map[string]string{"OPENAI_API_KEY": "fixture-key"}, check: ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "openai/gpt-5"}}},
		"cursor":   {binary: "cursor-agent", environment: "CURSOR_API_KEY CURSOR_DATA_DIR", localEnv: map[string]string{"CURSOR_API_KEY": "fixture-key"}},
		"devin":    {binary: "devin", environment: "DEVIN_API_KEY WINDSURF_API_KEY XDG_DATA_HOME", localEnv: map[string]string{"DEVIN_API_KEY": "fixture-key"}},
		"droid":    {binary: "droid", environment: "FACTORY_API_KEY AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_PROFILE AWS_DEFAULT_PROFILE AWS_CONFIG_FILE AWS_SHARED_CREDENTIALS_FILE AWS_WEB_IDENTITY_TOKEN_FILE AWS_ROLE_ARN AWS_BEARER_TOKEN_BEDROCK AWS_REGION AWS_DEFAULT_REGION", localEnv: map[string]string{"FACTORY_API_KEY": "fixture-key"}},
		"goose":    {binary: "goose", environment: `GOOSE_MODE GOOSE_PROVIDER GOOSE_DISABLE_KEYRING GOOSE_PATH_ROOT OPENAI_API_KEY ANTHROPIC_API_KEY GOOGLE_API_KEY OPENROUTER_API_KEY AIMLAPI_API_KEY DASHSCOPE_API_KEY CELERIS_API_KEY CEREBRAS_API_KEY DEEPSEEK_API_KEY EMPIRIOLABS_API_KEY EUROUTER_API_KEY FIREWORKS_API_KEY FRIENDLI_API_KEY FUTURMIX_API_KEY GROQ_API_KEY SPARK_API_PASSWORD ASTRON_API_KEY INCEPTION_API_KEY META_MODEL_API_KEY MINIMAX_API_KEY MISTRAL_API_KEY MOONSHOT_API_KEY NEARAI_API_KEY NOVITA_API_KEY NVIDIA_API_KEY OLLAMA_CLOUD_API_KEY OPENCODE_API_KEY OPPER_API_KEY ORCAROUTER_API_KEY OVHCLOUD_API_KEY PERPLEXITY_API_KEY PLEUMROUTER_API_KEY ROUTSTR_API_KEY SAKANA_API_KEY SALAD_CLOUD_API_KEY SAYGM_API_KEY SCW_SECRET_KEY TANZU_AI_API_KEY TANZU_AI_ENDPOINT TENSORIX_API_KEY TOGETHER_API_KEY TRUSTEDROUTER_API_KEY VENICE_API_KEY AI_GATEWAY_API_KEY ZHIPU_API_KEY GCP_PROJECT_ID GOOGLE_APPLICATION_CREDENTIALS AZURE_OPENAI_ENDPOINT AZURE_OPENAI_DEPLOYMENT_NAME AZURE_OPENAI_API_KEY AZURE_OPENAI_AD_TOKEN AZURE_TENANT_ID AZURE_CLIENT_ID AZURE_CLIENT_SECRET DATABRICKS_HOST DATABRICKS_TOKEN GITHUB_COPILOT_TOKEN GITHUB_COPILOT_HOST`, localEnv: map[string]string{"GOOSE_PROVIDER": "openai", "OPENAI_API_KEY": "fixture-key"}},
		"grok":     {binary: "grok", environment: "GROK_HOME GROK_DEFAULT_MODEL XAI_API_KEY GROK_DEPLOYMENT_KEY GROK_AUTH GROK_AUTH_PATH", localEnv: map[string]string{"XAI_API_KEY": "fixture-key"}},
		"kilocode": {binary: "kilo", environment: "KILO_CONFIG_CONTENT KILO_AUTH_CONTENT KILO_DB KILO_API_KEY KILOCODE_API_KEY OPENAI_API_KEY ANTHROPIC_API_KEY GEMINI_API_KEY GOOGLE_API_KEY OPENROUTER_API_KEY DEEPSEEK_API_KEY GROQ_API_KEY XAI_API_KEY MISTRAL_API_KEY COHERE_API_KEY KILO_CONFIG", localEnv: map[string]string{"OPENAI_API_KEY": "fixture-key"}, check: ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "openai/gpt-5"}}},
		"kimchi":   {binary: "kimchi", environment: "KIMCHI_API_KEY KIMCHI_CODING_AGENT_DIR", localEnv: map[string]string{"KIMCHI_API_KEY": "fixture-key"}},
		"kimi":     {binary: "kimi", environment: "KIMI_CODE_HOME KIMI_MODEL_NAME KIMI_MODEL_PROVIDER_TYPE KIMI_MODEL_API_KEY KIMI_SHARE_DIR KIMI_API_KEY OPENAI_API_KEY ANTHROPIC_API_KEY GOOGLE_API_KEY VERTEXAI_API_KEY KIMI_CODE_CUSTOM_HEADERS GOOGLE_CLOUD_PROJECT GOOGLE_CLOUD_LOCATION GOOGLE_APPLICATION_CREDENTIALS CLOUDSDK_CONFIG", localEnv: map[string]string{"KIMI_MODEL_NAME": "model", "KIMI_MODEL_PROVIDER_TYPE": "openai", "KIMI_MODEL_API_KEY": "fixture-key"}},
		"kiro":     {binary: "kiro-cli", environment: "KIRO_API_KEY", localEnv: map[string]string{"KIRO_API_KEY": "ksk_fixture"}, check: ports.AgentAuthCheck{Interactive: false}},
		"muse":     {binary: "muse", environment: "META_API_KEY MUSE_AUTH_PATH", localEnv: map[string]string{"META_API_KEY": "fixture-key"}},
		"omp":      {binary: "omp", environment: `PI_CODING_AGENT_DIR OMP_PROFILE PI_PROFILE PI_CONFIG_FILES OMP_AUTH_BROKER_URL OMP_AUTH_BROKER_TOKEN OMP_AUTH_BROKER_ACCOUNT_POOL_FILE OMP_AUTH_BROKER_SNAPSHOT_CACHE OMP_AUTH_BROKER_SNAPSHOT_TTL_MS AWS_BEDROCK_SKIP_AUTH AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_BEARER_TOKEN_BEDROCK GOOGLE_APPLICATION_CREDENTIALS GOOGLE_CLOUD_PROJECT GCP_PROJECT GCLOUD_PROJECT GOOGLE_VERTEX_LOCATION GOOGLE_CLOUD_LOCATION VERTEX_LOCATION ANTHROPIC_OAUTH_TOKEN ANTHROPIC_API_KEY AZURE_OPENAI_API_KEY BASETEN_API_KEY CEREBRAS_API_KEY CLOUDFLARE_AI_GATEWAY_API_KEY COPILOT_GITHUB_TOKEN GH_TOKEN GITHUB_TOKEN DEEPSEEK_API_KEY DEVIN_API_KEY FIREWORKS_API_KEY GEMINI_API_KEY GOOGLE_CLOUD_API_KEY GROQ_API_KEY HF_TOKEN HUGGINGFACE_HUB_TOKEN KILO_API_KEY LITELLM_API_KEY LM_STUDIO_API_KEY META_API_KEY MINIMAX_API_KEY MISTRAL_API_KEY MOONSHOT_API_KEY NVIDIA_API_KEY OLLAMA_API_KEY OLLAMA_CLOUD_API_KEY OPENAI_API_KEY OPENAI_CODEX_OAUTH_TOKEN OPENCODE_API_KEY OPENROUTER_API_KEY QWEN_OAUTH_TOKEN QWEN_PORTAL_API_KEY SAKANA_API_KEY SYNTHETIC_API_KEY TOGETHER_API_KEY VENICE_API_KEY AI_GATEWAY_API_KEY VLLM_API_KEY XAI_API_KEY XAI_OAUTH_TOKEN XIAOMI_API_KEY ZAI_API_KEY ZHIPU_API_KEY`, localEnv: map[string]string{"OPENAI_API_KEY": "fixture-key"}, check: ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "openai/gpt-5"}}},
		"opencode": {binary: "opencode", environment: "OPENCODE_CONFIG OPENCODE_CONFIG_CONTENT OPENCODE_CONFIG_DIR OPENCODE_DISABLE_PROJECT_CONFIG OPENCODE_API_KEY OPENAI_API_KEY ANTHROPIC_API_KEY GEMINI_API_KEY GOOGLE_API_KEY OPENROUTER_API_KEY DEEPSEEK_API_KEY GROQ_API_KEY XAI_API_KEY MISTRAL_API_KEY COHERE_API_KEY AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_BEARER_TOKEN_BEDROCK AWS_CONFIG_FILE AWS_SHARED_CREDENTIALS_FILE AWS_PROFILE AWS_DEFAULT_PROFILE AWS_ROLE_ARN AWS_WEB_IDENTITY_TOKEN_FILE", localEnv: map[string]string{"OPENAI_API_KEY": "fixture-key"}, check: ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "openai/gpt-5"}}},
		"pi": {binary: "pi", environment: `PI_CODING_AGENT_DIR ANTHROPIC_AUTH_TOKEN ANTHROPIC_OAUTH_TOKEN ANTHROPIC_API_KEY ANT_LING_API_KEY OPENAI_API_KEY AZURE_OPENAI_API_KEY DEEPSEEK_API_KEY NVIDIA_API_KEY GEMINI_API_KEY GROQ_API_KEY CEREBRAS_API_KEY XAI_API_KEY FIREWORKS_API_KEY TOGETHER_API_KEY BASETEN_API_KEY OPENROUTER_API_KEY AI_GATEWAY_API_KEY ZAI_API_KEY ZAI_CODING_CN_API_KEY MISTRAL_API_KEY MINIMAX_API_KEY MINIMAX_CN_API_KEY MOONSHOT_API_KEY HF_TOKEN OPENCODE_API_KEY KIMI_API_KEY QWEN_TOKEN_PLAN_API_KEY QWEN_TOKEN_PLAN_CN_API_KEY XIAOMI_API_KEY XIAOMI_TOKEN_PLAN_CN_API_KEY XIAOMI_TOKEN_PLAN_AMS_API_KEY XIAOMI_TOKEN_PLAN_SGP_API_KEY COPILOT_GITHUB_TOKEN RADIUS_API_KEY CLOUDFLARE_API_KEY CLOUDFLARE_ACCOUNT_ID CLOUDFLARE_GATEWAY_ID
AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_PROFILE AWS_DEFAULT_PROFILE AWS_CONFIG_FILE AWS_SHARED_CREDENTIALS_FILE AWS_WEB_IDENTITY_TOKEN_FILE AWS_ROLE_ARN AWS_BEARER_TOKEN_BEDROCK AWS_REGION AWS_DEFAULT_REGION
GOOGLE_CLOUD_API_KEY GOOGLE_CLOUD_PROJECT GCLOUD_PROJECT GOOGLE_CLOUD_LOCATION GOOGLE_APPLICATION_CREDENTIALS CLOUDSDK_CONFIG`, localEnv: map[string]string{"ANTHROPIC_API_KEY": "fixture-key"}, check: ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "anthropic/claude"}}},
		"prime-agent": {binary: "prime-agent", environment: `PRIME_AGENT_CODING_AGENT_DIR AO_DATA_DIR AWS_BEDROCK_SKIP_AUTH AWS_WEB_IDENTITY_TOKEN_FILE AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY GOOGLE_CLOUD_PROJECT GCLOUD_PROJECT GOOGLE_CLOUD_LOCATION GOOGLE_APPLICATION_CREDENTIALS ANTHROPIC_OAUTH_TOKEN ANTHROPIC_API_KEY COPILOT_GITHUB_TOKEN GH_TOKEN GITHUB_TOKEN OPENAI_API_KEY AZURE_OPENAI_API_KEY PRIME_API_KEY DEEPSEEK_API_KEY GEMINI_API_KEY GOOGLE_CLOUD_API_KEY GROQ_API_KEY CEREBRAS_API_KEY XAI_API_KEY OPENROUTER_API_KEY AI_GATEWAY_API_KEY ZAI_API_KEY MISTRAL_API_KEY MINIMAX_API_KEY MINIMAX_CN_API_KEY MOONSHOT_API_KEY HF_TOKEN FIREWORKS_API_KEY OPENCODE_API_KEY KIMI_API_KEY CLOUDFLARE_API_KEY XIAOMI_API_KEY XIAOMI_TOKEN_PLAN_CN_API_KEY XIAOMI_TOKEN_PLAN_AMS_API_KEY XIAOMI_TOKEN_PLAN_SGP_API_KEY`, localEnv: map[string]string{"OPENAI_API_KEY": "fixture-key"}, check: ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "openai/gpt-5"}}},
		"qwen":        {binary: "qwen", environment: "QWEN_HOME QWEN_CODE_SYSTEM_SETTINGS_PATH QWEN_CODE_SYSTEM_DEFAULTS_PATH OPENAI_API_KEY OPENAI_BASE_URL OPENAI_MODEL QWEN_MODEL ANTHROPIC_API_KEY ANTHROPIC_BASE_URL ANTHROPIC_MODEL GEMINI_API_KEY GEMINI_MODEL GOOGLE_API_KEY GOOGLE_MODEL GOOGLE_CLOUD_PROJECT GOOGLE_APPLICATION_CREDENTIALS CLOUDSDK_CONFIG QWEN_OAUTH", localEnv: map[string]string{"OPENAI_API_KEY": "fixture-key", "OPENAI_MODEL": "gpt-5"}},
		"vibe":        {binary: "vibe", environment: "MISTRAL_API_KEY VIBE_HOME VIBE_ACTIVE_MODEL", localEnv: map[string]string{"MISTRAL_API_KEY": "fixture-key"}},
	}

	for _, ha := range Harnessed() {
		if ha.Harness == "claude-code" || ha.Harness == "codex" {
			continue
		}
		test, ok := tests[ha.Harness]
		if !ok {
			t.Fatalf("missing authentication conformance inventory for %q", ha.Harness)
		}
		t.Run(string(ha.Harness), func(t *testing.T) {
			isolateAuthRoots(t)
			for _, name := range strings.Fields(test.environment) {
				t.Setenv(name, "")
			}
			installAuthTestBinary(t, test.binary)

			status, err := authStatusForTest(context.Background(), ha.Agent, ports.AgentAuthCheck{})
			if err != nil {
				t.Fatalf("isolated AuthStatus: %v", err)
			}
			if status != ports.AgentAuthStatusUnknown {
				t.Fatalf("isolated AuthStatus = %q, want unknown", status)
			}

			for name, value := range test.localEnv {
				t.Setenv(name, value)
			}
			status, err = authStatusForTest(context.Background(), ha.Agent, test.check)
			if err != nil {
				t.Fatalf("local-evidence AuthStatus: %v", err)
			}
			if status == ports.AgentAuthStatusAuthorized {
				t.Fatal("local evidence returned authorized")
			}
		})
	}
	if len(tests) != len(Harnessed())-2 {
		t.Fatalf("authentication inventory has %d entries for %d in-scope harnesses", len(tests), len(Harnessed())-2)
	}
}

func authStatusForTest(ctx context.Context, agent ports.Agent, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if scoped, ok := agent.(ports.AgentScopedAuthChecker); ok {
		return scoped.AuthStatusFor(ctx, check)
	}
	return agent.(ports.AgentAuthChecker).AuthStatus(ctx)
}

func isolateAuthRoots(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "APPDATA", "LOCALAPPDATA", "TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, root)
	}
}

func installAuthTestBinary(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	version := name + " 999.0.0"
	if name == "muse" {
		version = "Muse Code 999.0.0 (fixture)"
	}
	script := "#!/bin/sh\nif [ \"${1:-}\" = \"--version\" ] || [ \"${1:-}\" = \"version\" ]; then\n  printf '%s\\n' '" + version + "'\n  exit 0\nfi\nexit 1\n"
	if name == "goose" {
		script = "#!/bin/sh\nif [ \"${1:-}\" = \"--help\" ]; then\n  printf '%s\\n' 'Usage: goose [OPTIONS] <COMMAND>' 'Commands:' '  session  Manage sessions' '  recipe  Manage recipes'\n  exit 0\nfi\nexit 1\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake auth binary %q: %v", name, err)
	}
	for _, command := range []string{"az", "gh", "security", "secret-tool"} {
		commandPath := filepath.Join(dir, command)
		if err := os.WriteFile(commandPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatalf("write fake command %q: %v", command, err)
		}
	}
	t.Setenv("PATH", dir)
}

func TestRegistryIncludesPrimeAgent(t *testing.T) {
	reg, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := reg.Get("prime-agent")
	if !ok {
		t.Fatal("registry does not contain prime-agent")
	}
	manifest := adapter.Manifest()
	if manifest.Name != "Prime Agent" {
		t.Fatalf("prime-agent manifest name = %q, want Prime Agent", manifest.Name)
	}

	for _, item := range Harnessed() {
		if item.Harness == "prime-agent" {
			return
		}
	}
	t.Fatal("Harnessed does not contain prime-agent")
}

func TestRegistryIncludesOMP(t *testing.T) {
	reg, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := reg.Get("omp")
	if !ok {
		t.Fatal("registry does not contain omp")
	}
	manifest := adapter.Manifest()
	if manifest.Name != "OMP" {
		t.Fatalf("omp manifest name = %q, want OMP", manifest.Name)
	}

	for _, item := range Harnessed() {
		if item.Harness == domain.HarnessOMP {
			return
		}
	}
	t.Fatal("Harnessed does not contain omp")
}

func TestHarnessedExcludesFakeHarness(t *testing.T) {
	for _, ha := range Harnessed() {
		if ha.Harness == domain.HarnessFake {
			t.Fatal("fake harness must not be returned as a shipped selectable agent")
		}
	}
}

func TestEveryProductionHarnessReportsModelOrModeConfig(t *testing.T) {
	for _, ha := range Harnessed() {
		t.Run(string(ha.Harness), func(t *testing.T) {
			spec, err := ha.Agent.GetConfigSpec(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range spec.Fields {
				if field.Key == "model" || field.Key == "mode" {
					return
				}
			}
			t.Fatalf("%s exposes neither model nor mode configuration: %#v", ha.Harness, spec.Fields)
		})
	}
}

// workspaceFiles returns every regular file under root, relative to root.
func workspaceFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk workspace: %v", err)
	}
	return files
}

func ensureAgentBinary(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	binPath := filepath.Join(dir, name)
	version := "0.80.6"
	if name == "omp" {
		version = "17.1.0"
	}
	script := "#!/usr/bin/env sh\nif [ \"${1:-}\" = \"--version\" ]; then\n  echo \"" + name + " " + version + "\"\nfi\nexit 0\n"
	if err := os.WriteFile(binPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake agent binary %q: %v", binPath, err)
	}

	old := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+old)
}

func hasLine(content, line string) bool {
	for _, l := range strings.Split(content, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}
