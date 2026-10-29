# frozen_string_literal: true

# Run with `make spec`. Needs tmux installed: tmux.rb validates option names
# against the tmux on PATH while it compiles. scripts/test-tmux-conf.sh
# loads the compiled file into a real tmux server.
require "minitest/autorun"
require "tmpdir"
require_relative "../lib/tmux-ai-attn"

ENTRY = File.join(TmuxAiAttn::ROOT, "tmux-ai-attn.rb")
COMPILED = File.join(TmuxAiAttn::ROOT, "tmux-ai-attn.conf")

describe "tmux-ai-attn.conf" do
  it "is what tmux.rb compiles from tmux-ai-attn.rb" do
    expected = Tmux.load_file(ENTRY).to_tmux
    actual = File.read(COMPILED)

    assert expected == actual, <<~MESSAGE
      tmux-ai-attn.conf is out of date with lib/tmux-ai-attn.rb.
      Run `make conf` and commit the result.

      #{diff(expected)}
    MESSAGE
  end

  def diff(expected)
    Dir.mktmpdir do |dir|
      File.write(File.join(dir, "expected"), expected)
      IO.popen(["diff", "-u", "--label", "tmux-ai-attn.conf", "--label", "tmux.rb output",
        COMPILED, File.join(dir, "expected")], &:read)
    end
  end
end

describe TmuxAiAttn do
  def compile(&block) = Tmux.configure(&block).to_tmux

  it "can be used from a tmux.rb configuration" do
    conf = compile { use TmuxAiAttn }

    assert_includes conf, %(run-shell -b "\\"#{TmuxAiAttn::ROOT}/scripts/start-loop.sh\\" \\"#{TmuxAiAttn::ROOT}\\" --restart || true")
    assert_includes conf, %(if [ -x \\\\\\"#{TmuxAiAttn::ROOT}/scripts/start-loop.sh\\\\\\" ])
    assert_includes conf, %(if-shell -F "\#{==:\#{@ai_attn_seen_flash_seconds},}")
  end

  it "sets options it is given in place of their defaults" do
    conf = compile do
      append TmuxAiAttn.config(seen_flash_seconds: 30, enable_default_formats: true, color_text_fg: :default)
    end

    assert_includes conf, %(set-option -g @ai_attn_seen_flash_seconds "30")
    assert_includes conf, %(set-option -g @ai_attn_enable_default_formats "on")
    assert_includes conf, %(set-option -g @ai_attn_color_text_fg "default")
    refute_includes conf, %(\#{@ai_attn_seen_flash_seconds},})
    assert_includes conf, %(\#{==:\#{@ai_attn_tick_ms},})
  end

  it "rejects options it does not know" do
    error = assert_raises(ArgumentError) { TmuxAiAttn.config(seen_flash_secs: 30) }
    assert_match(/unknown tmux-ai-attn option: seen_flash_secs/, error.message)
  end

  it "uses the plugin directory it is given" do
    conf = compile { append TmuxAiAttn.config(plugin_dir: "/opt/tmux-ai-attn") }

    assert_includes conf, %(\\\\\\"/opt/tmux-ai-attn/scripts/start-loop.sh\\\\\\" \\\\\\"/opt/tmux-ai-attn\\\\\\")
    refute_includes conf, TmuxAiAttn::ROOT
  end

  it "offers its default window tabs as formats" do
    tab = Tmux.configure { window.status.format = TmuxAiAttn.window_status_format }.to_tmux

    assert_includes tab, "\#{?@ai_attn_window_waiting,\#{?@ai_attn_window_flash,\#{?@ai_attn_flash_phase,"
    assert_includes tab, " \#{window_index}:\#{window_name} \#[default]"
  end
end
