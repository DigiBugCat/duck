-- duck snap — Cmd+Shift+3 captures a screen selection, uploads it to the duck
-- hub, and copies the remote path to your clipboard. Paste that path into a
-- Claude Code session on the hub and ask about it; Claude reads the image by
-- path. All the logic lives in the `duck snap` subcommand — this is just the
-- hotkey.
--
-- Setup
--   1. brew install --cask hammerspoon
--   2. Copy this into ~/.hammerspoon/init.lua (or `dofile` it from there).
--   3. Free up Cmd+Shift+3: System Settings → Keyboard → Keyboard Shortcuts →
--      Screenshots → uncheck "Save picture of screen as a file".
--   4. Grant Hammerspoon Accessibility + Screen Recording
--      (System Settings → Privacy & Security).
--   5. Launch Hammerspoon → Reload Config.
--
-- (Prefer no extra app? macOS Shortcuts works too: a "Run Shell Script:
--  duck snap" shortcut with a keyboard shortcut, no daemon.)

hs.hotkey.bind({ "cmd", "shift" }, "3", function()
  -- Login shell so PATH includes ~/.local/bin, where the duck binary lives.
  hs.task.new("/bin/zsh", function(code, _, stderr)
    if code ~= 0 then
      hs.alert.show("duck snap failed:\n" .. (stderr or ""))
    end
  end, { "-lc", "duck snap" }):start()
end)

hs.alert.show("duck snap loaded (Cmd+Shift+3)")
