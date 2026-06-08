-- duck snap — Cmd+Shift+3 captures a screen selection, uploads it to the duck
-- hub, and copies the remote path to your clipboard. Paste that path into a
-- Claude Code session on the hub and ask about it; Claude reads the image by
-- path. All the logic lives in the `duck snap` subcommand — this is just the
-- hotkey.
--
-- MANAGED FILE — installed by `duck snap install-hotkey` from the binary's
-- embedded copy (assets/hammerspoon-snap.lua), so every Mac gets the identical
-- binding from git. Re-run install-hotkey after `duck update` to refresh it; do
-- not hand-edit (your changes are overwritten). `duck snap install-hotkey`
-- writes this to ~/.hammerspoon/duck-snap.lua and `dofile`s it from init.lua.
--
-- After installing: free Cmd+Shift+3 (System Settings → Keyboard → Keyboard
-- Shortcuts → Screenshots → uncheck "Save picture of screen as a file"), grant
-- Hammerspoon Accessibility + Screen Recording, then Reload Config.

hs.hotkey.bind({ "cmd", "shift" }, "3", function()
  -- Login shell so PATH includes ~/.local/bin, where the duck binary lives.
  hs.task.new("/bin/zsh", function(code, _, stderr)
    if code ~= 0 then
      hs.alert.show("duck snap failed:\n" .. (stderr or ""))
    end
  end, { "-lc", "duck snap" }):start()
end)

hs.alert.show("duck snap loaded (Cmd+Shift+3)")
