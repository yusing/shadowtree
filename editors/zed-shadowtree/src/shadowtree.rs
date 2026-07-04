use zed::Result;
use zed_extension_api as zed;

const SHADOWTREE_LSP_ID: &str = "shadowtree-lsp";
const SHADOWTREE_MODULE_DECL: &str = "module github.com/yusing/shadowtree";
const LOCAL_LSP_PATH: &str = "cmd/shadowtree-lsp/main.go";

struct ShadowtreeExtension;

impl zed::Extension for ShadowtreeExtension {
    fn new() -> Self {
        Self
    }

    fn language_server_command(
        &mut self,
        language_server_id: &zed::LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<zed::Command> {
        if language_server_id.as_ref() != SHADOWTREE_LSP_ID {
            return Err(format!("unknown language server ID {language_server_id}"));
        }

        if let Some(command) = worktree.which(SHADOWTREE_LSP_ID) {
            return Ok(zed::Command {
                command,
                args: vec![],
                env: worktree.shell_env(),
            });
        }

        if matches!(
            worktree.read_text_file("go.mod"),
            Ok(go_mod) if go_mod.lines().any(|line| line.trim() == SHADOWTREE_MODULE_DECL)
        ) && worktree.read_text_file(LOCAL_LSP_PATH).is_ok()
        {
            let command = worktree.which("go").ok_or_else(|| {
                "go must be installed to run the local Shadowtree LSP".to_string()
            })?;
            return Ok(zed::Command {
                command,
                args: vec!["run".into(), "./cmd/shadowtree-lsp".into()],
                env: worktree.shell_env(),
            });
        }

        Err("install shadowtree-lsp on PATH, or open the Shadowtree repository for local development".into())
    }
}

zed::register_extension!(ShadowtreeExtension);
