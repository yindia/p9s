# p9s: violation detail with remediation. Render with `make tapes` (needs vhs and `make e2e-up`); writes docs/image/violation-detail.gif.
Output docs/image/violation-detail.gif
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

# A Kyverno PSS violation: how to fix comes from the policy annotations
Type ":v"
Enter
Type "o"
Type "/require-team-label"
Enter
Sleep 1.5s
Type "d"
Sleep 6s
Type "y"
Sleep 3s
Escape
Escape

# A gatekeeper-library constraint: the template's description
Type "/K8sRequiredLabels"
Enter
Sleep 1.5s
Type "d"
Sleep 6s
Escape

Sleep 2s
Ctrl+C
