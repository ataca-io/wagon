# bash completion for wagon. Install:
#   wagon completion bash > ~/.local/share/bash-completion/completions/wagon
# Works on bash 3.2 (macOS): no mapfile, no compopt.

_wagon() {
	# Split the line up to the cursor on whitespace only, so an address such as
	# a@b.c or a --flag=value stays one word; COMP_WORDS splits at @, :, and =.
	local line=${COMP_LINE:0:COMP_POINT}
	local -a words
	read -r -a words <<<"$line"
	if [[ $line == *[[:space:]] ]]; then
		words[${#words[@]}]=""
	fi
	local cur=${words[${#words[@]}-1]}

	local out
	out=$(command wagon __complete "${words[@]:1}" 2>/dev/null)
	# Set after the call: bash 3.2 joins a quoted array slice on a newline IFS.
	local IFS=$'\n'
	if [[ $out == ":file" ]]; then
		COMPREPLY=($(compgen -f -- "$cur"))
	else
		COMPREPLY=($(compgen -W "$out" -- "$cur"))
	fi

	# bash replaces only the text after the last COMP_WORDBREAKS character in
	# cur, so drop everything up to it from each candidate.
	local i pre=
	for ((i = ${#cur} - 1; i >= 0; i--)); do
		if [[ $COMP_WORDBREAKS == *"${cur:i:1}"* ]]; then
			pre=${cur:0:i+1}
			break
		fi
	done
	if [[ -n $pre ]]; then
		for i in "${!COMPREPLY[@]}"; do
			COMPREPLY[i]=${COMPREPLY[i]#"$pre"}
		done
	fi
}

complete -o filenames -F _wagon wagon
