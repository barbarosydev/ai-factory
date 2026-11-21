# GitHub App Setup (Local macOS Keychain)

`ai-factory` authenticates with GitHub as a dedicated GitHub App using short-lived (1-hour) installation tokens minted via RS256 App JWTs ([ADR 0004](adr/0004-github-app-stdlib-auth-and-human-in-the-loop.md)). No personal access token (PAT) and no private key file remain on disk.

## 1. Create the GitHub App

1. Open GitHub Settings -> Developer settings -> **GitHub Apps** -> **New GitHub App**.
2. Set basic details:
   - **GitHub App name**: `barbarosydev-ai-factory` (or your own unique app name)
   - **Homepage URL**: `https://github.com/barbarosydev/ai-factory`
   - **Webhook**: Uncheck **Active** (the factory polls GitHub locally over outbound HTTPS; zero inbound ports or tunnels are needed).
3. Configure **Repository permissions**:
   - **Contents**: `Read and write` (push `factory/issue-<N>` branches, commit site files and screenshots)
   - **Issues**: `Read and write` (swap `factory:*` labels, post clarification questions and status comments, close rejected/completed issues)
   - **Pull requests**: `Read and write` (open PRs and squash-merge verified builds in `auto` mode)
   - **Workflows**: `Read and write` (allows GitHub App pushes that touch CI workflows cleanly if ever needed)
   - **Actions**: `Read-only`
   - **Checks**: `Read-only` (poll PR check runs before autonomous merge)
   - **Metadata**: `Read-only` (required default)
4. Set **Where can this GitHub App be installed?** -> **Only on this account**.
5. Click **Create GitHub App**.
6. Record the numeric **App ID** shown at the top of the app page.

## 2. Generate and Store the Private Key in macOS Keychain

1. In the left sidebar of your GitHub App settings, click **Credentials** -> open the **Key pairs** sub-tab (`/settings/apps/<your-app>/key_pairs`) -> click the green **New key** button (downloads `<your-app>.<date>.private-key.pem` to `~/Downloads/`).
2. Store the Base64-encoded PEM inside macOS Keychain and delete the `.pem` file from disk immediately:

   ```bash
   PEM_FILE="$(ls -t ~/Downloads/*.private-key.pem | head -n1)"
   security add-generic-password -U \
     -a "$USER" \
     -s "ai-factory-github-app-pem" \
     -w "$(base64 < "$PEM_FILE" | tr -d '\n')"
   rm -P "$PEM_FILE"
   ```

## 3. Install the App on `ai-factory-websites` Only

1. In the left sidebar of your GitHub App settings, click **Install App**.
2. Install it **only** on the selected repository: **`barbarosydev/ai-factory-websites`**.
   - Never grant installation access to `ai-factory` or `ai-factory-infra`.
3. Look at the browser URL after installation (`https://github.com/settings/installations/<INSTALLATION_ID>`) and note the numeric **Installation ID**.

## 4. Configure Non-Secret App Identifiers

Store the non-secret `GITHUB_APP_ID` and `GITHUB_INSTALLATION_ID` either in your shell profile or inside `~/.config/ai-factory/env` (outside any Git repository):

```bash
mkdir -p ~/.config/ai-factory
cat <<'EOF' > ~/.config/ai-factory/env
export GITHUB_APP_ID="<YOUR_APP_ID>"
export GITHUB_INSTALLATION_ID="<YOUR_INSTALLATION_ID>"
EOF
```

`scripts/doctor.sh`, `scripts/run.sh`, and `scripts/status.sh` automatically source `~/.config/ai-factory/env` when present and inject `GITHUB_APP_PRIVATE_KEY_BASE64` from macOS Keychain solely into the `./bin/factory` process environment at `exec` time.

## 5. Verify

```bash
task build
task doctor
```

`task doctor` should report `[PASS] github-app       installation token minted and barbarosydev/ai-factory-websites accessible`.
