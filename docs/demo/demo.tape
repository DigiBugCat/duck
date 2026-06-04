# VHS tape for the duck README demo GIF.
#   render with:  cd docs/demo && vhs demo.tape
# Produces ../img/demo.gif. Uses the sanitized ./duck stand-in on PATH so the
# recording shows generic projects + you@hub.local — never real hub data.

Output ../img/demo.gif

Set Shell "bash"
Set FontSize 20
Set Width 1100
Set Height 640
Set Padding 24
Set Theme "Catppuccin Mocha"
Set TypingSpeed 60ms
Set PlaybackSpeed 1.0

# Put the demo stand-in first on PATH for this recording only, and make the
# cd target exist so the prompt doesn't show an error (cleaned up after render).
Hide
Type "export PATH=$PWD:$PATH; mkdir -p ~/dev/auth-refactor; PROMPT_COMMAND=; PS1=$'\\e[2m❯\\e[0m '; clear" Enter
Show

Sleep 600ms
Type "cd ~/dev/auth-refactor" Sleep 400ms Enter
Sleep 500ms
Type "duck" Sleep 400ms Enter
Sleep 4s

Type "clear" Enter
Sleep 400ms

# The session picker: type-to-filter narrowing live.
Type "duck --resume" Sleep 400ms Enter
Sleep 5s

Sleep 1s
