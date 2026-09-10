import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { apiClient, apiErrorMessage } from "../../lib/api-client";
import { SettingsOptionMenu } from "./SettingsOptionMenu";

export function ClaudeProfileSelect({
  value,
  onChange,
}: {
  value: string;
  onChange: (value: string) => void;
}) {
  const { t } = useTranslation();
  const query = useQuery({
    queryKey: ["claude-profiles"],
    queryFn: async () => {
      const { data, error } = await apiClient.GET(
        "/api/v1/agents/claude-code/profiles",
      );
      if (error) throw new Error(apiErrorMessage(error));
      return data?.profiles ?? [];
    },
  });
  const options = [
    { value: "__inherit__", label: t("settings.project.claudeProfileInherit") },
    ...(query.data ?? []).map((profile) => ({
      value: profile.configDir,
      label: `Claude Code — ${profile.name}`,
    })),
  ];
  if (!options.some((option) => option.value === value)) {
    options.push({
      value,
      label: value
        ? t("settings.project.claudeProfileSaved", { path: value })
        : t("settings.project.claudeProfileDefault"),
    });
  }
  return (
    <div>
      <SettingsOptionMenu
        aria-label={t("settings.project.claudeProfile")}
        value={value}
        options={options}
        onChange={onChange}
      />
      {query.isError && (
        <p role="status" className="text-sm text-error">
          {t("settings.project.claudeProfilesLoadFailed")}
        </p>
      )}
    </div>
  );
}
