# Marketing videos

`yottacode` can help turn raw screen recordings and local assets into publishable marketing cuts by driving local `ffmpeg`/`ffprobe` tools through the agent. The recording workflow targets yottacode demos: inspect the recording, find dead air or visually idle terminal stretches, propose cuts, then render YouTube/X-ready MP4s and short GIF previews after approval. Prompt mode can also draft and render asset-based release videos from docs, logos, screenshots, and supplied clips.

## Dependencies

Install the external binaries yourself; yottacode detects them but does not bundle or auto-install them.

| Binary | Required | Used for |
|---|---:|---|
| `ffprobe` | yes | `media_probe` metadata inspection |
| `ffmpeg` | yes | `media_analyze` audio/visual detectors, `media_compose` assembly, and `media_render` output |
| `whisper` / `whisper-cli` | optional | local transcript/caption generation before rendering |

Check readiness with:

```bash
yottacode doctor
```

The doctor output includes a **Media Editing** section with install hints for missing binaries.

## Slash-command shortcut

Use `/video` in the TUI as a guided entry point:

```text
/video
/video out/demo.mp4
/video edit out/demo.mp4
/video analyze out/demo.mp4
/video prompt Create a 45-second release video from RELEASE_v0.3.0.md and marketing/raw/demo.mp4
```

Bare `/video` explains the available video capabilities. With a path, it sends a normal agent workflow prompt that uses the same public tools (`media_probe`, `media_analyze`, `media_render`) and the same approval gates. With `prompt <goal>`, it asks the agent to plan an asset-based marketing video from local docs, screenshots, title cards, and clips, then stop for approval before rendering with `media_compose` / `media_render`. Natural language remains supported; `/video` is only a shortcut for discoverability. See [`video-tools.md`](video-tools.md) for the full tool/capability matrix.

## Prompt-driven marketing videos

Use `/video prompt <goal>` when you want a finished marketing-video plan rather than cleanup for one recording:

```text
/video prompt Create a 60-second v0.4.0 release video from CHANGELOG.md, RELEASE_v0.4.0.md, and marketing/raw/dispatch-demo.mp4
/video prompt Make a 20-second X teaser from marketing/raw/demo.mov highlighting subagents and worktrees
/video prompt Create a YouTube tutorial intro for plan mode using screenshots in marketing/assets/
```

Prompt mode is still asset-based. The model reads referenced docs and assets, writes a storyboard/script, proposes segment order and output profiles, and asks for approval before calling any render tool. After approval it can use `media_compose` to assemble title cards, screenshots/images, and approved clip segments into a draft MP4 with branded templates, lower-third captions, simple fades, and image zoom/pan motion, then use `media_render` for platform exports.

This does not require an AI video model. Hosted text-to-video, generated b-roll, TTS narration, and music generation are optional future layers; the current workflow uses a language model for planning/copy and local `ffmpeg` for deterministic assembly.

## Recommended workflow

1. Record the raw demo with your normal screen recorder.
2. Put the source file under the project, for example `marketing/raw/demo.mp4`.
3. Ask yottacode to inspect it:

   ```text
   Probe marketing/raw/demo.mp4 and tell me the duration, resolution, and audio setup.
   ```

4. Ask it to find fluff/dead air:

   ```text
   Analyze marketing/raw/demo.mp4 for terminal demo idle time and propose cuts. Use mode=auto. Do not render until I approve the edit plan.
   ```

5. Review the proposed cut list. Ask for changes if a pause should stay.
6. Render the final outputs:

   ```text
   Render the approved edit plan to YouTube 16:9, X 16:9, and GIF preview outputs under marketing/out/.
   ```

For silent terminal recordings, `media_analyze` automatically falls back to visual idle detection. You can force the terminal-tuned thresholds with:

```text
Analyze marketing/raw/demo.mp4 with media_analyze mode=terminal_demo and show candidate cuts; do not render yet.
```

## Output profiles

| Profile | Size | Use |
|---|---:|---|
| `youtube_16x9` | 1920×1080 | YouTube demos and long-form posts |
| `x_16x9` | 1920×1080 | X timeline video posts |
| `x_vertical_9x16` | 1080×1920 | Vertical X clips / shorts-style derivatives |
| `gif_preview` | 960px wide, 12 fps | Short looping GIF teasers for docs/social snippets |
| `gif_preview_large` | 1440px wide, 12 fps | More readable terminal GIF previews |

When multiple profiles are requested from one base output, yottacode appends the profile name to the file stem, for example `demo-youtube_16x9.mp4`, `demo-x_16x9.mp4`, and `demo-gif_preview.gif`. GIF output uses ffmpeg palette generation for better quality, but GIFs grow quickly; use `gif_preview` or `gif_preview_large` for short approved clips rather than full-length demos. Ask for `speed=1.5` or `speed=2` when a terminal GIF should move faster without changing the approved cut ranges.

## Captions and branding

`media_compose` can build title-card/image/clip drafts from local assets with branded templates, lower thirds, image zoom/pan motion, and simple fades. `media_render` accepts an optional captions file path and burns subtitles into the output with ffmpeg. Keep brand assets in the repo, for example under `marketing/assets/`, so normal path validation and review apply.

## Current boundaries

This is not a full timeline editor and it does not generate AI video clips. It edits and composes real local assets with ffmpeg utilities. Generated feature videos, TTS narration, and stylized AI clips remain separate roadmap work in `roadmap/video-generation.md`.
