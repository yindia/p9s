# p9s: p9s report. Render with `make tapes` (needs vhs and `make e2e-up`); writes docs/image/report.gif.
Output docs/image/report.gif
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

Type "p9s --context kind-p9s report --format json -o /tmp/p9s-r1.json"
Enter
Sleep 7.5s
Type "p9s --context kind-p9s report --since /tmp/p9s-r1.json | head -40"
Enter
Sleep 7.5s
Type "p9s --context kind-p9s report --fail-on critical > /dev/null; echo exit=$?"
Enter
Sleep 7.5s
