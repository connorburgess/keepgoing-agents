# Security

## Reporting a vulnerability

Use GitHub private vulnerability reporting on this repository. Do not open a public issue for a security problem. Include the version, the configuration shape with secrets removed, and steps to reproduce.

## What this tool touches

keepgoing-agents runs the commands you configure, reads their structured output, writes state files under its own state directory, and posts signed webhooks to the URL you configure. It never edits or merges repositories. Secrets are read from environment variables or files you point it at and are never written to logs or reports.
