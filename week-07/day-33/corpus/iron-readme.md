# iRon — lightweight overlays for iRacing

> This is a user-facing documentation excerpt for support retrieval, based on product sections of the `iRon-ai-review` checkout snapshot at source commit `cb76b89633554e1dd7f288087054f30b00bd9a55`. The source repository is [VaSeWS/iRon_fork](https://github.com/VaSeWS/iRon_fork); source file: [`README.md`](https://github.com/VaSeWS/iRon_fork/blob/cb76b89633554e1dd7f288087054f30b00bd9a55/README.md). This is an explicit source snapshot selection, not a chronological comparison. AI-review-only content is excluded from this excerpt. This corpus is synthetic support material, not evidence of a Windows runtime test.
>
> The upstream README says: “I created iRon for my own personal use. As such, its feature set is limited to what I considered sensible in practice given the way I use iRacing. I don't currently plan to extend it further. That said, I'm making it available in the hope it might be useful to others in the iRacing community, either for direct use or as a starting point for other homebrew overlays.”
>
> **Attribution and license:** iRon upstream source files are attributed to L. E. Spalt; the upstream `LICENSE` identifies the project as MIT License, Copyright (c) 2021–2022 L. E. Spalt. See [the upstream LICENSE](https://github.com/VaSeWS/iRon_fork/blob/cb76b89633554e1dd7f288087054f30b00bd9a55/LICENSE). This documentation excerpt is provided for support retrieval and does not replace or modify the upstream license.

## Product overview

This project provides a few lightweight overlays for iRacing. Included are: a relative display with optional minimap, a dashboard with fuel calculator, a throttle/brake input graph, and a standings display. The project's code base aims to be small, easy to modify, and free of external dependencies.

## Relative

Like the *Relative* box in iRacing, but with additional information such as license, iRating, and laps driven since the last pit stop. You can also highlight your friends by adding their names to a buddy list.

At the top is an optional minimap (`minimap_enabled`). It can be set to either relative mode (own car fixed in the center) or absolute mode (start/finish line fixed in the center) via the `minimap_is_relative` parameter: `true` for relative mode, `false` for absolute mode.

## Inputs

Shows throttle/brake/steering in a moving graph. The author says it is useful for practicing consistent braking.

## Standings

Shows the standings of the entire field, including safety rating, iRating, and number of laps since the last pit stop (“pit age”). The author usually leaves it off by default and switches it on during cautions, or looks at it before a race to get a sense of the competition level.

Like the “Relative” overlay, it will highlight buddies in green.

## Delta

A live time-delta trace plots, across the lap, how much time you are gaining or losing against a reference lap. Green areas mean you are up on the reference; red means you are down. An optional *ghost* trace—a dashed line with a faint neutral fill—overlays a second reference lap for comparison. A *section bar* along the bottom splits the lap into the sim's official timing sectors and colours each by time gained or lost. When a trace runs past the visible vertical scale its line fades out, leaving only the filled area pinned to the top or bottom edge. When you set a new best lap, the ghost's lap becomes the reference itself, so the ghost smoothly collapses onto the centre line and merges with it; an optional gold pulse of the centre line marks the personal best.

Some parameters accept a fixed set of values rather than a number or colour:

| Parameter | Allowed values | Meaning |
|---|---|---|
| `target` | `session_best` (default), `best` | Reference lap used for the live delta. `session_best` is your fastest lap in the current session; `best` is the sim's stored best lap. |
| `ghost` | `off` (default), `last`, `best`, `session_best` | Which lap to draw as the dashed ghost trace. `off` disables it; `last` is your last completed lap; `best` and `session_best` are your best lap (currently identical). |

Other notable parameters:

| Parameter | Type / range | Default | Meaning |
|---|---|---|---|
| `delta_range_sec` | float, min `0.05` | `1.0` | Minimum vertical scale (floor): ± this many seconds at most zoom. The scale is dynamic—it eases out lap by lap to fit the largest delta seen—but never zooms in tighter than this. |
| `range_steps` | list of floats | `["0.25","0.5","1","1.5","2","3","5","10"]` | Discrete scale steps (seconds) the dynamic scale can land on. Steps at or below `delta_range_sec` are dropped; the floor is always the lowest step. |
| `range_headroom` | float, min `0` | `0.08` | Extra slack above the peak delta before choosing a larger step (`0.08` = +8%). |
| `rescale_speed` | float, clamped `0.1`–`10` | `1.0` | Multiplier on how fast the scale animates. Zoom-out is fast so peaks are not clipped; zoom-in is slower. |
| `trace_resolution` | int, clamped `16`–`5000` | `300` | Number of sample buckets across the lap (higher means a smoother trace and more work per frame). |
| `line_thickness` | float | `2.0` | Thickness of the delta trace line. |
| `ghost_thickness` | float | `line_thickness` | Thickness of the ghost trace line. |
| `section_bar` | bool | `true` | Show the per-sector delta bar at the bottom. |
| `section_bar_height` | float | `22.0` | Height of the section bar, in pixels. |
| `show_section_values` | bool | `true` | Print the time gained or lost in each section on the bar. |
| `new_best_pulse` | bool | `true` | Briefly pulse the centre line when you set a new best lap. Set `false` to disable just the pulse; the ghost still collapses onto the centre. |

All `*_col` parameters (for example `gain_col`, `loss_col`, `ghost_col`, `ghost_fill_col`, `zero_line_col`, `new_best_pulse_col`, and `section_gain_col`) are RGBA colours given as four numbers in the range `[0, 1]`. `ghost_fill_col` defaults to faint white (`1 1 1 0.12`); `new_best_pulse_col` defaults to warm gold (`1 0.85 0.35 0.55`).

## Installing and running

The app does not require installation. Copy the executable to a folder of your choice. Make sure the folder is not write-protected, as iRon attempts to save its configuration file in the working directory.

Run the executable. It does not matter whether you do this before or after launching iRacing. A console window will appear, indicating that iRon is running. Once you are in the car in iRacing, the overlays should show up, and you can configure them. The README recommends running iRacing in borderless window mode. Overlays might work in other modes, but the author has not tested that.

## Configuration

To place and resize the overlays, press **Alt+J**. This enters a mode in which you can move overlays with the mouse and resize them by dragging their bottom-right corner. Press **Alt+J** again to return to normal mode.

Overlays can be switched on and off at runtime using the hotkeys displayed during startup. All hotkeys are configurable.

Certain aspects of the overlays, such as colours, font types, and sizes, can be customized by editing the `config.json` file iRon created. Changes can be made while the app is running and take effect immediately whenever the file is saved.

Most parameters are self-explanatory: colours are RGBA values given as four numbers in `[0, 1]`, sizes are in pixels, and `true`/`false` parameters switch a feature on or off. Some parameters accept a fixed set of named values; the README documents the allowed values in each overlay's section (for example, Delta's `target` and `ghost`).

**Important:** the config file is created only after the overlays have been “touched” for the first time, usually by dragging or resizing them. The config file is saved in the working directory.

## Building from source and dependencies

The app is built with Visual Studio 2022. The free version should suffice, though the author has not verified it. The project/solution files should work out of the box. Depending on the Visual Studio setup, additional prerequisites (static libs) needed to build DirectX applications may be required.

There are no runtime dependencies other than standard Windows components such as DirectX, which should already be present on most if not all systems that can run iRacing. Build dependencies (most notably the iRacing SDK and picojson) are kept to a minimum and are included in the repository.

## Source locations

Source README sections reproduced above: `README.md` lines 3–7, 61–63, 67–69, 75–77, 83–108, 114–116, 122–130, and 136–144 in the selected `iRon-ai-review` checkout snapshot; its AI-review-only material is deliberately excluded. Source license: `LICENSE` lines 1–21. Source repository URL and exact selected revision are recorded in `manifest.json`.
