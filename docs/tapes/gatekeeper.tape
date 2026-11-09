# p9s: Gatekeeper engine views. Render with `make tapes` (needs vhs and `make e2e-up`); writes docs/image/gatekeeper.gif.
Output docs/image/gatekeeper.gif
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

Type "p9s --context kind-p9s -c gk/ct"
Enter
Sleep 4s

# A ConstraintTemplate's highlighted Rego and parameters schema
Type "/K8sReplicaLimits"
Enter
Sleep 1.5s
Type "d"
Sleep 6s
Escape
Sleep 1s

# Its constraint, then the constraint's match, parameters and violations
Enter
Sleep 3s
Type "d"
Sleep 5.5s
Ctrl+C
