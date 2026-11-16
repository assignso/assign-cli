---
dependencies:
  - name: Go
    url: https://go.dev/doc/devel/release
  - name: Cobra
    url: https://github.com/spf13/cobra/releases
  - name: Go Keyring
    url: https://github.com/zalando/go-keyring/releases
  - name: Bun
    url: https://github.com/oven-sh/bun/releases
  - name: Commander.js
    url: https://github.com/tj/commander.js/releases
  - name: Node.js
    url: https://nodejs.org/en/about/previous-releases
  - name: TypeScript
    url: https://github.com/microsoft/TypeScript/releases
  - name: Ink
    url: https://github.com/vadimdemedes/ink/releases
  - name: React
    url: https://react.dev/blog
  - name: assistant-ui
    url: https://github.com/assistant-ui/assistant-ui/releases
  - name: Model Context Protocol
    url: https://modelcontextprotocol.io/specification/
maintain:
  - path: .
    manager: Go modules
    update:
      - go get <module>@<version>
    sources:
      - https://go.dev/doc/devel/release
      - https://go.dev/doc/modules/managing-dependencies
  - path: command
    manager: Bun
    update:
      - bun update <package>
    sources:
      - https://github.com/oven-sh/bun/releases
      - https://bun.com/docs/pm/cli/update
  - path: terminal
    manager: npm
    update:
      - npm update <package> --package-lock-only --ignore-scripts
    sources:
      - https://github.com/npm/cli/releases
      - https://docs.npmjs.com/cli/v11/commands/npm-update/
---

# Dependency watch
