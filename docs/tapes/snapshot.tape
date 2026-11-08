# p9s: snapshots and offline replay. Render with `make tapes` (needs vhs and `make e2e-up`); writes docs/image/snapshot.gif.
Output docs/image/snapshot.gif
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

Type "p9s --context kind-p9s snapshot -o /tmp/p9s-demo.json"
Enter
Sleep 9s
Type "p9s --snapshot /tmp/p9s-demo.json get pol | head -6"
Enter
Sleep 4.5s
Type "p9s --snapshot /tmp/p9s-demo.json"
Enter
Sleep 4.5s
Type ":v"
Enter
Sleep 3s
Enter
Sleep 3s
Type "d"
Sleep 4.5s
Ctrl+C
