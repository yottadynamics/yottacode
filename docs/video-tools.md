# Video tools and capabilities

`yottacode` can plan and render local marketing-video assets through `ffmpeg` and `ffprobe`. The video tools are deterministic local renderers, not hosted AI video generators: the model drafts the story and approved edit plan, while the tools inspect, analyze, compose, and export local files.

## Tool map

| Tool | Needs approval | Input | What it can do |
|---|---:|---|---|
| `media_probe` | no | One local audio/video file | Report duration, streams, codecs, resolution, frame rate, rotation, and audio setup with `ffprobe` |
| `media_analyze` | no | One local audio/video file | Find candidate dead air/fluff using audio silence and visual idle/freezedetect detectors |
| `media_compose` | yes | Approved storyboard segments | Assemble title cards, images/screenshots, and clips into a draft MP4 with templates, captions, fades, and image motion |
| `media_render` | yes | Approved recording or draft MP4 | Export YouTube/X MP4 profiles, vertical clips, GIF previews, caption burn-in, and optional intro/outro assembly |

## What we can create

### Asset-based release videos

Use `/video prompt <goal>` when you have release notes, screenshots, logos, and optional raw clips. The agent should read the source material, propose a storyboard, and wait for approval before rendering.

Good inputs:

- `RELEASE_*.md`, `CHANGELOG.md`, README sections, or launch notes
- product screenshots, cover art, logos, and social-card images
- optional terminal recordings or demo clips

Possible outputs:

- release announcement videos
- feature teasers for X/LinkedIn
- YouTube intro/outro drafts
- short GIF previews for docs or social posts

### Recording cleanup

Use `/video <path>` or `/video analyze <path>` when you already have a local recording. The agent should probe/analyze the recording, propose conservative cut ranges, and wait for approval before rendering.

Good inputs:

- `.mp4`, `.mov`, `.mkv`, or `.webm` screen recordings
- terminal demos with or without audio
- raw product walkthrough clips

Possible outputs:

- cleaned YouTube 16:9 demos
- X 16:9 clips
- vertical 9:16 derivatives
- readable GIF loops

## `media_compose` segment capabilities

`media_compose` renders an ordered storyboard into a draft `.mp4`. It validates every source path as a read, validates the output as a write, refuses accidental overwrites unless `overwrite=true`, and requires approval before running ffmpeg.

| Segment | Required fields | Optional polish |
|---|---|---|
| `title` | `text`, `duration` | `template`, `transition` |
| `image` | `path`, `duration` | `caption`, `motion`, `template`, `transition` |
| `clip` | `path` | one `keep_ranges` entry, `caption`, `template`, `transition` |

Supported polish fields:

| Field | Values | Effect |
|---|---|---|
| `template` | `default`, `hero`, `feature`, `closing` | Adds simple branded green accents and sizing suitable for title, feature, or closing cards |
| `caption` | text | Adds a lower-third text overlay on image or clip segments |
| `motion` | `none`, `zoom_in`, `zoom_out` | Adds a gentle Ken Burns-style zoom to image segments |
| `transition` | `none`, `fade` | Adds conservative fade-in/out for synthetic-duration segments and fade-in for clips |

Safety bounds:

- output must end with `.mp4`
- title/image duration must be positive and at most 300 seconds per segment
- canvas is bounded to 16..7680px wide and 16..4320px high
- frame rate is bounded to 1..120 fps
- complex clip editing stays in `media_render`; `media_compose` accepts at most one keep range per clip

## `media_render` export profiles

Use `media_render` after a recording cleanup or after `media_compose` creates a draft MP4.

| Profile | Output | Use |
|---|---|---|
| `youtube_16x9` | 1920×1080 MP4 | YouTube demos and long-form release videos |
| `x_16x9` | 1920×1080 MP4 | X/LinkedIn timeline posts |
| `x_vertical_9x16` | 1080×1920 MP4 | Shorts-style derivatives |
| `gif_preview` | 960px GIF, 12 fps | Short docs/social previews |
| `gif_preview_large` | 1440px GIF, 12 fps | More readable terminal demos |

`media_render` can also burn in captions, prepend intro/outro clips, render sped-up GIFs, and apply approved `cut_ranges` or `keep_ranges`.

## Resource usage

`media_analyze`, `media_render`, and `media_compose` all shell out to `ffmpeg`/`ffprobe`. These are real CPU-bound subprocesses running on your machine, not sandboxed or metered by default — a large render (long source clips, multiple export profiles in one call, or `gif_preview_large`'s palette-generation filter chain) can run for minutes and compete with everything else on your desktop for CPU.

Every ffmpeg invocation from these tools is bounded two ways:

- **Thread cap.** Decode, filter, and encode threads are capped (`-threads`/`-filter_threads`/`-filter_complex_threads`), so a render can't unilaterally claim every core on the host. Default 2; override with `[media] max_threads = N` in `config.toml`.
- **Scheduling priority.** Every ffmpeg/ffprobe child process runs at a lowered OS scheduling priority (nice), so it yields to your desktop compositor and other interactive work under contention instead of competing with them at equal priority. This is best-effort and silently skipped if the OS refuses it — it never blocks the render.
- **Wall-clock timeout.** Each invocation is bounded so a malformed filter graph or unexpectedly huge input can't hang a tool call indefinitely. Default 30 minutes; override with `[media] render_timeout_seconds = N`.

```toml
[media]
max_threads = 2
render_timeout_seconds = 1800
```

Practical guidance:

- For a new edit, render one profile against the approved range before rendering every requested profile — `/video`'s workflow does this by default.
- Prefer `gif_preview` over `gif_preview_large` unless you specifically need the larger/longer variant; it's the most CPU-intensive profile, and `media_render`'s approval prompt flags it (and any 3+ profile request) explicitly.
- When combining multiple recordings, render in two stages: first use `media_compose` with approved per-clip ranges to produce one draft MP4, then inspect that draft before running a final `media_render` export. Avoid composing multiple videos and immediately rendering multiple final profiles in the same approval; that stacks the two heaviest ffmpeg jobs back-to-back.

## Boundaries

This workflow does not generate synthetic video scenes, AI b-roll, voiceover, or music by itself. Those can be added later as separate provider/tool integrations. Today, yottacode can make polished local asset-based videos from the materials you provide and the storyboard you approve.
