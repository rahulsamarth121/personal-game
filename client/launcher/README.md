# Personal Cloud Gaming — launcher shell (Stage 8)

The custom client is an orchestration shell around Moonlight, not a new
streaming client:

- login, game library, Play, session info, history, playtime, settings
- hands off to Moonlight for video decode, audio, input, gamepad

Do not reimplement streaming here. See `docs/architecture/overview.md`.
