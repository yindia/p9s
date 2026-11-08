# p9s: p9s get. Render with `make tapes` (needs vhs and `make e2e-up`); writes docs/image/get.gif.
Output docs/image/get.gif
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

Type "p9s --context kind-p9s get pol | head -8"
Enter
Sleep 6s
Type "p9s --context kind-p9s get v -n p9s-demo --filter engine:kyverno"
Enter
Sleep 6s
Type "p9s --context kind-p9s get impact require-team-label -o json | head -20"
Enter
Sleep 6s
Type "p9s --context kind-p9s get health | head -12"
Enter
Sleep 6s
