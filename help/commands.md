# Commands

Commands are executed from COMMAND-LINE mode. Press : in NORMAL mode to enter
COMMAND-LINE mode, then type the command and press Enter.

  :new              - Start a new conversation
  :resume           - Resume a previous session
  :edicts            - Manage edicts (read, enact, seal, resume, cancel)
  :continue         - Resume a paused ritual on the current tab
  :abort            - Abort a paused ritual on the current tab
  :quit / :q       - Close current tab (quit app if last tab)
  :qa / :quitall    - Quit the application (all tabs)
  :update           - Check for and install updates

## Navigation

  :switch <target>  - Move the Court to a branch or edict worktree in place
                      e123   → edict 123's worktree (branch asimi/e123); created if missing
                      main   → the main working tree
                      branch → a named branch, resolved to its worktree when one exists
                      Refused while a tab is streaming; resets tabs to their defaults

## Information

  :help [topic]     - Show help (optionally for a specific topic)
  :context          - Show context usage and token information

## History

  :export [type]    - Export conversation to file and open in $EDITOR
                      Types: conversation (default), full

## Configuration

  :models           - Select AI model
  :login            - Authenticate with an AI provider

  :init [clean]     - Initialize project with infrastructure files
                      Creates: AGENTS.md, Justfile, .agents/Sandbox

## Examples

  :help modes       - Show help about vi modes
  :new              - Start fresh conversation
  :export           - Export and edit conversation
  :context          - Check token usage
