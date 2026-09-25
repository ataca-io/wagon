# fish completion for wagon. Install:
#   wagon completion fish > ~/.config/fish/completions/wagon.fish

function __wagon_complete
    set -l words (commandline -opc)
    set -l cur (commandline -ct)
    set -q cur[1]; or set cur ""
    set -l out (command wagon __complete $words[2..-1] $cur 2>/dev/null)
    if test "$out" = ":file"
        __fish_complete_path $cur
        return
    end
    printf '%s\n' $out
end

complete -c wagon -f -a '(__wagon_complete)'
