# frozen_string_literal: true

require "tmux"

# tmux-ai-attn's tmux configuration, written with tmux.rb
# (https://github.com/cosmicbuffalo/tmux.rb).
#
# This is the source of the plugin's tmux configuration. `make conf` compiles
# it, through tmux-ai-attn.rb, into tmux-ai-attn.conf, which
# tmux-ai-attn.tmux loads, so installing the plugin never needs Ruby.
#
# A tmux.rb configuration can include the plugin directly:
#
#   require "tmux-ai-attn"   # tmux.rb finds TPM-installed plugins by name
#
#   Tmux.configure do
#     use TmuxAiAttn                                    # the defaults
#     append TmuxAiAttn.config(seen_flash_seconds: 30)  # or with options
#   end
module TmuxAiAttn
  # The plugin checkout this file belongs to.
  ROOT = File.expand_path("..", __dir__)

  # tmux-ai-attn.tmux sets this hidden tmux environment variable to the plugin
  # checkout before it loads tmux-ai-attn.conf. tmux expands it while loading
  # the file, so the hooks it registers hold the checkout's absolute path,
  # which pmux's doctor and ai-attn-setup.sh look for in `show-hooks`.
  PLUGIN_DIR_VARIABLE = "${TMUX_AI_ATTN_DIR}"

  # Each option's default, applied when it is unset or holds the '' a stopped
  # daemon leaves behind. The keys name the @ai_attn_* options.
  DEFAULTS = {
    cli: "ai-attn",
    dev_build: "off",
    seen_flash_seconds: "3",
    enable_default_formats: "off",
    refresh_client: "on",
    icon_waiting: "⚠",
    icon_stopped: "⏸",
    icon_done: "✓",
    color_waiting: "yellow",
    color_stopped: "colour196",
    color_working: "#d88786",
    color_done: "green",
    color_flash_bg: "colour226",
    color_flash_fg: "colour16",
    color_text_fg: "colour255",
    spinner_frames: "·,·,✢,✳,✶,✽,✻,✻,✻,✽,✶,✳,✢,·",
    tick_ms: "120",
    flash_multiplier: "4"
  }.freeze

  # Events that start the sync loop for a server that does not have one yet.
  HOOKS = %w[client-attached client-session-changed session-created after-new-session].freeze

  # The hook array slot the plugin owns, so loading the configuration again
  # replaces its hooks rather than adding copies.
  HOOK_INDEX = 50

  # The hook body. Hooks must not hard-fail `tmux new-session` (or any other
  # triggering command) if the plugin is moved or deleted after registration:
  # `run-shell -b` keeps the exit status from reaching the command that fired
  # the hook, and the script checks the plugin at fire time, logging a
  # failure and showing a transient message instead of blocking. {dir} marks
  # the plugin directory; everything else reaches the shell as written.
  HOOK_SCRIPT = <<~'SH'.lines.map(&:strip).join(" ")
    log="${TMUX_AI_ATTN_LOG:-${XDG_STATE_HOME:-$HOME/.local/state}/tmux-ai-attn/hooks.log}";
    mkdir -p "${log%/*}" 2>/dev/null;
    if [ -x "{dir}/scripts/start-loop.sh" ]; then
      "{dir}/scripts/start-loop.sh" "{dir}" >> "$log" 2>&1 || {
        printf "%s [hook] start-loop.sh failed\n" "$(date +%Y-%m-%dT%H:%M:%S)" >> "$log" 2>/dev/null;
        tmux display-message "[tmux-ai-attn] hook failed (see $log)";
      };
    else
      printf "%s [hook] plugin missing: %s\n" "$(date +%Y-%m-%dT%H:%M:%S)" "{dir}/scripts/start-loop.sh" >> "$log" 2>/dev/null;
      tmux display-message "[tmux-ai-attn] plugin missing (see $log)";
    fi
  SH

  class << self
    # The plugin with its defaults, for `use TmuxAiAttn`.
    def to_config = config

    # The plugin's configuration. `plugin_dir` is the checkout holding its
    # scripts; tmux expands a $NAME in it when it loads the configuration.
    # Other keywords set @ai_attn_* options (see DEFAULTS), replacing any value
    # already set; true and false become "on" and "off".
    def config(plugin_dir: ROOT, hook_index: HOOK_INDEX, **options)
      unknown = options.keys - DEFAULTS.keys
      unless unknown.empty?
        raise ArgumentError, "unknown tmux-ai-attn option#{"s" if unknown.size > 1}: #{unknown.join(", ")} " \
          "(known: #{DEFAULTS.keys.join(", ")})"
      end

      options = options.to_h { |key, value| [option_name(key), option_value(value)] }
      defaults = DEFAULTS.to_h { |key, value| [option_name(key), value] }.except(*options.keys)
      scripts = "#{plugin_dir}/scripts"
      quiet = ">/dev/null 2>&1 || true"
      hook_command = hook_command(plugin_dir)
      enable_default_formats = Tmux.user_option(:@ai_attn_enable_default_formats)
      tab = window_status_format
      current_tab = window_status_current_format

      Tmux.configure do
        # Settings first: everything below reads them.
        options.each { |name, value| global[name] = value }
        defaults.each do |name, value|
          if?(Tmux.user_option(name) == "") { global[name] = value }
        end

        # Then activation. These run in order, without -b, so the sync loop
        # starts with exactly the settings made before the plugin loads, as
        # it did when only TPM loaded it; failures go to @ai_attn_last_error.
        run_shell %("#{scripts}/prepare-hooks.sh" "#{plugin_dir}" #{hook_index} #{quiet})
        run_shell %("#{scripts}/start-loop.sh" "#{plugin_dir}" --restart #{quiet})
        HOOKS.each { |event| hook event, hook_command, index: hook_index }

        if?(enable_default_formats == "on") do
          global.status.interval = 1
          window.status.format = tab
          window.status.current_format = current_tab
        end
      end
    end

    # The window tab @ai_attn_enable_default_formats installs: flashing while
    # an attention signal is unseen, then a steady color while it waits.
    def window_status_format = window_tab(current: false)

    # The same, for the current window's tab.
    def window_status_current_format = window_tab(current: true)

    private

    def option_name(key) = :"@ai_attn_#{key}"

    def option_value(value)
      case value
      when true then "on"
      when false then "off"
      else value.to_s
      end
    end

    # A `run-shell -b` command running HOOK_SCRIPT, written for the parse
    # `set-hook` makes of it: the script is escaped to reach the shell as
    # written, while a $NAME in the plugin directory is left for tmux to
    # expand.
    def hook_command(plugin_dir)
      dir = plugin_dir.gsub(/[\\"]/) { "\\#{_1}" }
      script = HOOK_SCRIPT.split("{dir}", -1).map { |part| part.gsub(/[\\"$]/) { "\\#{_1}" } }
      %(run-shell -b "#{script.join(dir)}")
    end

    def window_tab(current:)
      waiting = Tmux.user_option(:@ai_attn_window_waiting)
      flash = Tmux.user_option(:@ai_attn_window_flash)
      phase = Tmux.user_option(:@ai_attn_flash_phase)
      fg = current ? :colour16 : :colour231
      flash_on, flash_off = current ? %i[colour226 colour208] : %i[colour160 colour52]

      Tmux.format do
        if?(waiting) do
          if?(flash) do
            if?(phase) { style fg:, bg: flash_on, attrs: :bold }
              .else { style fg:, bg: flash_off, attrs: :bold }
          end.else do
            style fg:, bg: flash_off
          end
        end.else do
          current ? style(fg: :colour255, bg: :colour24, attrs: :bold) : style(fg: :colour244)
        end
        text " #{Tmux.window_index}:#{Tmux.window_name} "
        reset_style
      end
    end
  end
end
