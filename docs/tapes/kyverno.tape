# p9s: Kyverno engine views. Render with `make tapes` (needs vhs and `make e2e-up`); writes docs/image/kyverno.gif.
Output docs/image/kyverno.gif
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

Type "p9s --context kind-p9s -c kyv/cpol"
Enter
Sleep 4s
Type "d"
Sleep 4.5s
Escape
Sleep 1s

# The CEL policies.kyverno.io kinds
Type ":kyv/vpol"
Enter
Sleep 3s
Type "d"
Sleep 5s
Escape
Sleep 1s

# PolicyReports per resource, then mutations
Type ":kyv/polr"
Enter
Sleep 4s
Type ":mut"
Enter
Sleep 3.5s
Ctrl+C
