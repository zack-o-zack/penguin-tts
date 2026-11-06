# Distribution and local inference: research notes

Date: 2026-11-06. Scope: planning only, no app code. Sources were fetched over HTTP from official or primary pages on this date. No benchmarks were run, so no performance numbers below are measured on our hardware.

Primary-agent verification: GitHub's releases API confirms Wails v3.1.0 is the latest non-prerelease (2026-11-01). A direct refetch of the Wails home page returned HTTP 403; its home-page wording below remains the researcher's reported observation. Rechecking Chatterbox's repository README corrected the original claim about missing MPS support and surfaced its retirement notice. Apple payment interpretations below are qualified to account for exceptions.

Labels: **[Established]** = quoted or directly stated in a primary source. **[Interpretation]** = our reading, not stated by the source.

## Summary

- Mac App Store (MAS) is feasible only if the app is sandboxed and self-contained. The rule that matters most for in-app model downloads is Guideline 2.4.5(iv) and 2.5.2. Its wording is ambiguous for model weights. Do not assume approval.
- Wails v3 is the stable Go option (v3.0.0 documented as stable on Windows, macOS, Linux). v3.1.0 is marked Latest on GitHub. Streaming audio over its IPC is not documented, so it needs a spike.
- Kokoro-82M weights are Apache-2.0. Run it through sherpa-onnx (Apache-2.0) with a native C/Go/Swift binding, not Python. Its multilingual claims are broader than its documented language support. The phonemizer data may bring in GPL-3.0 obligations (see section 3).
- Chatterbox Multilingual (0.5B, MIT, 23+ languages) is a possible larger-model evaluation candidate. Its Python examples include CPU and MPS, but the repository README says the project is retired and no longer maintained. It embeds an audio watermark in every output.
- Recommend a bounded packaging/inference proof (section 4). Measure before promising any latency, quality, or calendar estimate.

## 1. Mac App Store: sandbox, downloads, helpers, paid tier

### 1a. Downloads and executable code

- **[Established]** Guideline 2.4.5 (Mac App Store only): apps "must be appropriately sandboxed"; "must be packaged and submitted using technologies provided in Xcode... self-contained, single app installation bundles and cannot install code or resources in shared locations." Source: https://developer.apple.com/app-store/review/guidelines/
- **[Established]** 2.4.5(iv): apps "may not download or install standalone apps, kexts, additional code, or resources to add functionality or significantly change the app from what we see during the review process." Same source.
- **[Established]** 2.4.5(iii): no auto-launch, and no processes that "continue to run without consent after a user has quit the app." Same source.
- **[Established]** 2.5.2: apps "may not read or write data outside the designated container area, nor may they download, install, or execute code which introduces or changes features or functionality of the app." Same source.
- **[Interpretation]** Model weights are data, not code, so 2.5.2 may not apply. But 2.4.5(iv) names "resources" and "to add functionality." A downloaded TTS voice model plausibly does both. Model files that a runtime interprets (ONNX graphs) may be read as code. Neither reading is confirmed by Apple.
- **[Interpretation]** Lower-risk design: ship at least one default model inside the signed bundle so the app works offline on first launch. Treat extra models as optional content, disclosed clearly in App Review notes. Ask App Review directly before relying on this.

### 1b. Helper processes and runtime packaging

- **[Established]** Apple documents embedding a command-line tool in a sandboxed app. The helper is placed in the app's `Contents/MacOS`. It must be signed with App Sandbox and `com.apple.security.inherit` only. Adding other entitlements "can cause problems"; the tool then "immediately crashes with a code signing error." `com.apple.security.get-task-allow` is incompatible with inherit. Source: https://developer.apple.com/documentation/xcode/embedding-a-helper-tool-in-a-sandboxed-app.md
- **[Established]** Apple states the App Sandbox must be enabled to distribute through the Mac App Store. Source: https://developer.apple.com/documentation/security/app-sandbox.md
- **[Interpretation]** A sherpa-onnx helper (or a Go worker) embedded in the bundle and launched by the app fits the documented pattern. Keep it inside the bundle, sandboxed, and terminated when the app quits (2.4.5(iii)).
- **[Interpretation]** A bundled Python runtime adds dependencies and signing/sandbox validation work. This research establishes neither a blanket ban on bundled interpreters nor acceptance of this particular arrangement. Installing packages after delivery is a separate code-download concern.
- **[Unverified]** Whether App Review accepts the GPL-3.0 phonemizer data (section 3) inside the MAS bundle. Ask the GPL question with a lawyer, not App Review.

### 1c. Paid service and BYOK

- **[Established]** 3.1.1: to "unlock features or functionality within your app" you "must use in-app purchase"; apps "may not use their own mechanisms to unlock content... such as license keys." Source: https://developer.apple.com/app-store/review/guidelines/
- **[Established]** 2.4.5(vi) (MAS): apps "may not present a license screen at launch, require license keys, or implement their own copy protection."
- **[Established]** 3.1.1(a): links to outside purchase are restricted outside the US storefront; "in all other storefronts... apps and their metadata may not include buttons, external links, or other calls to action that direct customers to purchasing mechanisms other than in-app purchase," unless an entitlement is granted. Same source.
- **[Established]** 3.1.3 lists exceptions to 3.1.1, including 3.1.3(f) for qualifying free companion apps to certain paid web tools. Eligibility of this TTS service is not established. Same Apple guidelines source.
- **[Interpretation]** Plan a separate purchase/entitlement boundary for a future hosted service. Recheck its actual features, storefronts, and channels against current rules; do not assume IAP-only is a universal conclusion. A direct-download build has a separate notarization and update path.
- **[Interpretation]** Connecting an existing provider API key is technically distinct from selling provider credits or unlocking Penguin features. This research does not establish blanket App Review approval of BYOK.
- **[Established]** 2.4.5(vii): MAS apps "must use the Mac App Store to distribute updates; other update mechanisms are not allowed." This matters if the app self-updates or downloads new code.

**Not approval.** These are the written rules as of this fetch. App Review decides case by case, and Apple says guidelines are "a living document."

## 2. Wails: stability, Go suitability, IPC constraints

- **[Established]** Wails v3.0.0 is "the stable release for desktop applications on Windows, macOS and Linux"; iOS and Android are experimental. Source: https://v3.wails.io/
- **[Established]** GitHub marks **v3.1.0** as "Latest" (released 01 Nov 2026). It lists "experimental CGO-free macOS and Linux backends." Nightly v3.2.0 builds exist. Source: https://github.com/wailsapp/wails/releases
- **[Established]** Wails v2.17.1 (01 Nov 2026) fixes a Linux iframe IPC issue (GHSA-r5j2-ghh9-2jqw) affecting v2.17.0 and earlier. The v3 migration page says "v2 will receive critical updates." Sources: same releases page; https://v3.wails.io/migration/v2-to-v3/
- **[Established]** Wails v3 uses OS WebViews, with "In-memory IPC. No network ports." Source: https://v3.wails.io/
- **[Established]** Events: "Don't use events for large data—use bindings"; "Don't use events for RPC." Event handlers receive sequential `Emit` calls in order, but `Emit` never waits for handlers. Source: https://v3.wails.io/features/events/system/
- **[Interpretation]** Streaming synthesized audio chunks is not covered by any documented pattern. Options are events (documented as wrong for large data), bindings (RPC-style), or serving chunks through the asset handler. Each must be measured in the spike.
- **[Established]** Go suitability: sherpa-onnx documents a Go API and C API. Source: https://github.com/k2-fsa/sherpa-onnx
- **[Unverified]** Wails documentation on Mac App Store submission, sandbox entitlements, or helper embedding. We did not find it in primary sources. Assume it is not documented.
- **[Interpretation]** Wails is a reasonable fit for a general-audience UI in Go. Ship on macOS first, and verify the CGO-free backend before depending on it for sandboxed builds.

## 3. Kokoro-82M and local inference options

### 3a. Licensing

- **[Established]** Kokoro-82M: "open-weight TTS model with 82 million parameters," license "apache-2.0." Source: https://huggingface.co/hexgrad/Kokoro-82M
- **[Established]** Training data is described as "permissive/non-copyrighted audio" plus IPA labels, and "synthetic audio... generated by closed TTS models from large providers." Same source. Provenance is documented but not audited by us.
- **[Established]** The model card lists some CC BY 3.0 and CC BY 4.0 training audio. Same source. Which attribution obligations apply to downstream distribution must be checked against the actual artifacts and licenses; training-data licenses do not automatically establish obligations for every generated output.
- **[Established]** sherpa-onnx is Apache-2.0. Source: https://github.com/k2-fsa/sherpa-onnx
- **[Established]** kokoro-onnx (Python wrapper) is MIT; "kokoro model: Apache 2.0." Source: https://github.com/thewh1teagle/kokoro-onnx
- **[Established]** eSpeak NG (used for phonemization in sherpa's Kokoro example via `--kokoro-data-dir espeak-ng-data`) is "GPL version 3 or later." Source: https://github.com/espeak-ng/espeak-ng
- **[Interpretation]** Inventory the exact phonemizer library/data shipped and assess its license and linkage with the rest of the app. Being open source alone does not settle GPL compliance or compatibility with store distribution terms. Check whether an alternative G2P path avoids the dependency; do not assume process isolation removes license obligations.

### 3b. Runtime options

- **[Established]** sherpa-onnx Kokoro release is `kokoro-multi-lang-v1_0`, with `model.onnx` (310M), `voices.bin` (26M), and an int8 variant. Source: https://k2-fsa.github.io/sherpa/onnx/tts/pretrained_models/kokoro.html
- **[Established]** kokoro-onnx: "Lightweight: ~300MB (quantized: ~80MB)"; a maintainer claim of "near real-time on macOS M1" with no measurement shown. Source: https://github.com/thewh1teagle/kokoro-onnx
- **[Established]** The cited sherpa page reports Raspberry Pi 4 Kokoro RTF: 7.635 (1 thread) down to 3.191 (4 threads). RTF above 1 is slower than real time. This is not an M1 measurement. Source: same sherpa page.
- **[Established]** sherpa documents multilingual Kokoro support, but "we only add English and Chinese support for it." Same source. The HF card says "Languages: Multiple" and points to VOICES.md (not fetched).
- **[Established]** Native paths: sherpa-onnx lists C++, C, Python, Go, Swift, Rust, Dart, and more; it also documents Kokoro, KittenTTS, PocketTTS, SupertonicTTS, Piper, Matcha, and VITS. Source: https://k2-fsa.github.io/sherpa/onnx/tts/pretrained_models/kokoro.html and https://github.com/k2-fsa/sherpa-onnx
- **[Interpretation]** Native sherpa-onnx is preferable to Python for a shipped desktop app: no interpreter to bundle, and a clean C API callable from Go or Swift.

### 3c. Larger candidate: Chatterbox Multilingual

- **[Established]** Chatterbox Multilingual V3: 0.5B parameters, 23+ languages, MIT license, "Llama backbone," voice cloning and exaggeration control. Source: https://huggingface.co/ResembleAI/chatterbox
- **[Established]** Every output includes Resemble's PerTh watermark (imperceptible neural watermark). Same source.
- **[Established]** The repository README documents `pip install chatterbox-tts` and explicitly shows `device = "cuda" # or "cpu" / "mps"` for English and multilingual examples. Source: https://github.com/resemble-ai/chatterbox/blob/master/README.md
- **[Established]** The same README says: "As of August 2026, Chatterbox is retired research and is no longer maintained." This makes maintenance status a selection concern. A documented MPS option is not a measured M1 performance result.
- **[Established]** Single-language finetunes exist (Chinese, LatAm Spanish, pt-BR, es-ES, pt-PT, Hindi). Same source.
- **[Interpretation]** Chatterbox offers broader documented language coverage, but this research does not establish superior quality. MPS is documented; performance, memory, packaging, and maintenance suitability remain unproven. Keep it as an evaluation candidate rather than the default larger-model choice.

### 3d. Multilingual limits (summary)

- Kokoro: broad voice list in weights, but documented runtime support is English and Chinese (sherpa). Other languages need verification with real samples.
- Chatterbox: documented 23 languages, but the quality of each is untested by us.
- Neither source gives per-language quality metrics we can cite.

## 4. Recommended bounded packaging/inference proof

1. **Packaging proof.** Build a signed, sandboxed macOS app bundle with an embedded helper (per Apple's helper doc). Put one reviewed local model inside the bundle. Verify it launches, synthesizes, and passes `codesign` checks. Submit a test archive to App Store Connect if access is available.
2. **Download-policy proof.** Test a model download into the app container. Write the exact App Review question. Do not assume the answer.
3. **Latency proof.** On a real M1 machine, measure real-time factor, first-audio latency, and memory for Kokoro int8 and fp32 at representative thread counts. Report measured values only.
4. **Audio delivery proof.** Test a suitable binary/asset delivery path from Go to the frontend, with small progress events. Measure ordering, throughput, cancellation, and resource cleanup rather than presuming JSON events are appropriate for audio.
5. **Larger models, later.** Evaluate a maintained larger-model candidate after the first local path works. Chatterbox's documented MPS path is a possible experiment, but its retirement status argues against making it the baseline.
6. **License check.** Decide whether eSpeak NG (GPL-3.0+) can be avoided or isolated. Involve counsel before shipping.

Exit criteria: a signed sandboxed build with one bundled model, a measured RTF table, a chosen IPC pattern, and a written App Review question.

## 5. Uncertainties and open risks

- Whether Apple treats downloaded model weights as "resources to add functionality" (2.4.5(iv)). Unresolved by any source we read.
- Whether a bundled GPL-3.0 eSpeak NG (or a helper containing it) is acceptable in a MAS build.
- Whether the Wails v3 CGO-free backend works with sandboxed MAS builds. Not documented.
- Whether Wails has a documented streaming API for audio. None found in primary docs.
- Kokoro's real multilingual quality (documented: English and Chinese in sherpa; wider in weights).
- Chatterbox on Apple Silicon: MPS is documented, but no measurements were made; maintenance is an additional concern.
- Paid-service design: exact obligations depend on feature, channel, storefront, and applicable exceptions. No billing implementation is chosen.
- All performance statements in this file are unmeasured or from non-M1 hardware. Do not quote them externally.
