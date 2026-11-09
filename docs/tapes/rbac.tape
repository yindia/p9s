# p9s: namespace-only access. Render with `make tapes` (needs vhs and `make e2e-up`); writes docs/image/rbac.gif.
# Runs p9s as the p9s-tenant ServiceAccount from hack/manifests/tenant.yaml (view on p9s-demo).
Output docs/image/rbac.gif
Require kubectl
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

# A kubeconfig for the tenant, off camera (a TokenRequest; nothing else is created)
Hide
Type@5ms "export KUBECONFIG=/tmp/p9s-tenant.kubeconfig; kubectl config view --raw --minify --context kind-p9s --kubeconfig ~/.kube/config > $KUBECONFIG && kubectl config set-credentials p9s-tenant --token=$(kubectl --kubeconfig ~/.kube/config --context kind-p9s -n p9s-demo create token p9s-tenant) >/dev/null && kubectl config set-context p9s-tenant --cluster=kind-p9s --user=p9s-tenant --namespace=p9s-demo >/dev/null && kubectl config use-context p9s-tenant >/dev/null; clear"
Enter
Sleep 3s
Show

# The overview still works with view on one namespace
Type "p9s"
Enter
Sleep 6s

# Forbidden lists become notices; the rest still renders
Type ":v"
Enter
Sleep 4.5s
Type ":pol"
Enter
Sleep 4s
Ctrl+C

Hide
Type@5ms "rm -f /tmp/p9s-tenant.kubeconfig; unset KUBECONFIG"
Enter
Sleep 500ms
