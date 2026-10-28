#!/bin/sh
set -eu

# ScoreCalcFunction writes to $RCRS_HOME/logs/log/scores.log. Route that path
# to the launcher's chosen stage log directory without changing any server
# classes. Keep the link outside /work/logs, whose archive rejects symlinks.
find_log_dir() {
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --log)
                [ "$#" -gt 1 ] || return 0
                printf '%s' "$2"
                return 0
                ;;
            --log=*)
                printf '%s' "${1#--log=}"
                return 0
                ;;
        esac
        shift
    done
}

if [ "${1:-}" = "-jar" ] && [ "${2:-}" = "/opt/rcrs-server/jars/rcrs-server-launcher.jar" ]; then
    rcrs_log_dir="$(find_log_dir "$@")"
    if [ -n "$rcrs_log_dir" ]; then
        case "$rcrs_log_dir" in
            /*) ;;
            *) rcrs_log_dir="$PWD/$rcrs_log_dir" ;;
        esac
        mkdir -p "$rcrs_log_dir"
        rcrs_score_home="$(mktemp -d /tmp/simworkflow-score-home.XXXXXX)"
        mkdir -p "$rcrs_score_home/logs"
        ln -s "$rcrs_log_dir" "$rcrs_score_home/logs/log"
        export RCRS_HOME="$rcrs_score_home"
        # Explicit root also supports distributions without the maps/ directory
        # used by the launcher's automatic root detection.
        set -- "$@" --root /opt/rcrs-server
    fi
fi

exec "$JAVA_HOME/bin/java" "$@"
