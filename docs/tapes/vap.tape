# p9s: native ValidatingAdmissionPolicies. Render with `make tapes` (needs vhs and `make e2e-up`); writes docs/image/vap.gif.
Output docs/image/vap.gif
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

Type "p9s --context kind-p9s -c vap/pol"
Enter
Sleep 4.5s
Type "d"
Sleep 6s
Escape
Enter
Sleep 3s
Escape
Type "G"
Sleep 4.5s
Type ":health"
Enter
Sleep 4.5s
Ctrl+C
