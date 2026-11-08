# p9s: enforcement impact. Render with `make tapes` (needs vhs and `make e2e-up`); writes docs/image/impact.gif.
Output docs/image/impact.gif
Require p9s
Set Shell zsh
Set Theme "Catppuccin Mocha"
Set FontSize 15
Set LineHeight 1.25
Set Width 1600
Set Height 1000
Set Padding 28
Set Margin 24
Set MarginFill "#11111b"
Set WindowBar Colorful
Set BorderRadius 10
Set TypingSpeed 90ms

Type "p9s --context kind-p9s"
Enter
Sleep 4.5s

# A capped Gatekeeper constraint: showing 20 of M
Type ":gk/c"
Enter
Type "/container-must-meet-memory-and-cpu-ratio"
Enter
Sleep 1.5s
Type "i"
Sleep 4.5s
Enter
Sleep 3s
Escape
Escape
Escape

# A Kyverno policy in audit mode, by name
Type ":impact require-team-label"
Enter
Sleep 4.5s
Escape

# From the shared policy list
Type ":pol"
Enter
Type "/mode:audit"
Enter
Sleep 1.5s
Type "i"
Sleep 4.5s

Sleep 2s
Ctrl+C
