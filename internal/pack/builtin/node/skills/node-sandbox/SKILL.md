---
name: node-sandbox
description: How to install packages, build and test Node.js/TypeScript code in this agent-office sandbox (Debian's Node.js and npm, packages only from registry.npmjs.org). Use before running npm or node commands.
---

# Node.js in the agent-office sandbox

You run in a network-restricted container. What that means for Node.js:

- **Toolchain:** Node.js and npm from Debian (`node --version` to check). Do not try to install
  another Node version (nvm, corepack downloads): those hosts are blocked.
- **Packages:** only `registry.npmjs.org` is reachable. Dependencies from git URLs (GitHub) and
  other registries fail. Use `npm ci` when a `package-lock.json` exists, and commit the updated
  lock file when you add a dependency.
- **The npm cache** lives under `$HOME`, which is temporary.
- **Before you commit**, run the project's own checks, usually:
  ```sh
  npm run --if-present lint
  npm run --if-present typecheck
  npm test
  ```
