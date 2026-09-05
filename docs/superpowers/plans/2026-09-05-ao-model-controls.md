# AO model and permission controls

User-approved design: use the supplied Claude menu as AO's common permission UI across harnesses. Model selection is family then exact available version; selecting a family alone never changes the model. Keep provider aliases distinguishable from pinned versions and preserve provider identifiers unchanged. Astra must be discovered through the installed compatible Codex runtime, not injected as a fictitious catalog entry.

1. Continue PR4923 model work and PR4930 permission work in their existing isolated worktrees. Preserve the original dirty checkout.
2. Introduce tested family projection shared by compact new-task and chat model choices; retain the larger settings picker's recent/provider groups. Preserve unknown models, provider namespaces, searching, custom entries, selected/unlisted identity, and reasoning resets on selection.
3. Group both native Codex model choices and ACP model configuration choices using the same AO component. Keep other provider configuration dimensions unchanged.
4. Expose supported explicit Claude versions alongside aliases using evidence from provider capabilities; distinguish supported selectors from account entitlement. Invalidate cached catalogs when discovery changes.
5. Update the actual installed Codex CLI at its existing npm prefix and refresh discovery. Reconnect local review sessions so retained old processes do not keep the old model list.
6. Permission implementation in PR4930: fixed Auto/Manual/Accept Edits/Don't Ask/Bypass Permissions presentation. Map real provider operations, disable unsupported mappings, keep provider-default restoration explicit and preserve the native baseline safety fix. New Codex modes require service/driver/API tests.
7. Run focused regression tests, typechecks, relevant Go tests and browser tests; independently review both implementations. Launch a local-only combined review checkout, verify Claude/Codex menus and Astra execution, capture fresh screenshots, update the existing PRs and leave the app running.
