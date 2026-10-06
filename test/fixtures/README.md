# Reference fixtures

These immutable records preserve command output, terminal cells, encrypted repository objects and asset bytes captured before replacement. They aren't regenerated from the implementation under test.

| Folder | Contract |
| --- | --- |
| `cli/` | 61 external commands per recorded platform, 49 connected workflows, 68 layout records, colour profiles and format boundaries |
| `tui/` | Browser, setup, restore, help and game frames, model inputs and Unicode widths |
| `core/` | Existing encrypted objects, keyed chunk boundaries, signature vectors and BIP39 vectors |
| `assets.json` | SHA-256 hashes of the original wordmarks and sound effects |

Windows and Linux captures are recorded separately where output depends on the platform. Mac screen tests use the portable recorded Linux inputs. Platform-specific command comparisons skip when native captures are missing.

Don't update expected output to make a failed comparison pass. Fix the cause, or review an intentional behaviour change with the user first. All keys and credentials in the fixtures are public dummy test values.

## Reviewed changes

| Date | Frames | Change |
| --- | --- | --- |
| 2026-10-06 | `setup-11-check-*`, `setup-12-check-wrong-*` | The phrase check title reads "(1 of 2)" instead of using a hyphen as a dash. |
| 2026-10-06 | `10-game-title-050`, `11-game-play-050`, `12-game-over-050`, `game-*-20` | The game's window hint gives the real minimum, 36x22, instead of 34x18. |

Each change kept the text's length, so only those characters differ. Both apply to the Windows and Linux captures.
