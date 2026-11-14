# Contributing

Thanks for helping improve `sbs`. Bug reports, fixes and new features are all
welcome.

## Reporting a problem

Open an [issue](https://github.com/sandboxstudiosoftware/sandbox-studio-cli/issues)
with:

- the output of `sbs version`,
- your Sandbox Studio version,
- what you ran, what you expected, and what happened (`--debug` output helps).

Remove tokens, account IDs and other secrets before posting.

## Sending a change

1. Fork the repository and create a branch.
2. Make your change and add or update tests.
3. Check that everything passes:

   ```sh
   go vet ./...
   go test ./... -race
   go build ./...
   ```

4. Open a pull request that describes what the change does and why.

For larger changes, open an issue first so we can agree on the approach.

## Security issues

Please don't report security problems in public issues. Email
[dev@sandboxstudiosoftware.com](mailto:dev@sandboxstudiosoftware.com) instead.

## Licence

By contributing, you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE), the same licence as the project.
