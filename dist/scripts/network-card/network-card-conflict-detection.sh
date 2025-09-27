#!/bin/bash
set -e
set -o pipefail

# shellcheck disable=SC1091
source "$(dirname "${BASH_SOURCE[0]}")/../util/log.sh"
# shellcheck disable=SC1091
source "$(dirname "${BASH_SOURCE[0]}")/../util/util.sh"
# shellcheck disable=SC1091
source "$(dirname "${BASH_SOURCE[0]}")/../util/network.sh"
# shellcheck disable=SC1091
source "$(dirname "${BASH_SOURCE[0]}")/../util/curl.sh"

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${DIR}" || exit

log_info "Start network card vlan conflict detection"
YAML=$(generate_yaml_detection "network_card_conflict_results")$'\n'

# get the bond network card
set +e
log_debug "Start getting all bond interfaces"
bonds=$(get_bond_interfaces)
ret=$?
log_debug "$(echo "$bonds" | tr '\n' ' ')"
set -e

if [ $ret -ne 0 ] || [ -z "$bonds" ]; then
	log_err "No bond interfaces found"
	YAML+=$(generate_yaml_entry "No bond" "" "There is no bond network card, so there is no VLAN conflict" "warn")$'\n'
	log_debug "$YAML"
	# shellcheck disable=SC2154
	RESULT=$(echo "$YAML" | jinja2 network-card.j2 -D NodeName="$NodeName" -D Timestamp="$Timestamp")
	log_result "$RESULT"

	set +e
	log_debug "Start posting detection result"
	response=$(send_post "$EIS_POST_URL" "$RESULT" admin)
	# response=$(send_post "$EIS_POST_URL" "$RESULT" admin)
	ret=$?
	log_debug "$(echo "$response" | tr '\n' ' ')"
	set -e

	exit $ret
fi

# get the provider-networks.kubeovn.io bonding interface
set +e
log_debug "Start getting all provider-networks bonding interfaces"
default_interfaces=$(get_provider_networks)
ret=$?
log_debug "$(echo "$default_interfaces" | tr '\n' ' ')"
set -e

if [ $ret -eq 2 ]; then
	log_warn "Get provider-networks failed"
	YAML+=$(generate_yaml_entry "provider-networks " "Failed" "Get provider-networks failed" "warn")$'\n'
	log_debug "$YAML"
	# shellcheck disable=SC2154
	RESULT=$(echo "$YAML" | jinja2 network-card.j2 -D NodeName="$NodeName" -D Timestamp="$Timestamp")
	log_result "$RESULT"

	set +e
	log_debug "Start posting detection result"
	response=$(send_post "$EIS_POST_URL" "$RESULT" admin)
	# response=$(send_post "$EIS_POST_URL" "$RESULT" admin)
	ret=$?
	log_debug "$(echo "$response" | tr '\n' ' ')"
	set -e
	exit $ret
fi

if [ $ret -eq 1 ]; then
	log_err "There is no provider-networks resource found"
	YAML+=$(generate_yaml_entry "provider-networks" "Not found" "There is no provider-networks resource found" "error")$'\n'
	log_debug "$YAML"
	# shellcheck disable=SC2154
	RESULT=$(echo "$YAML" | jinja2 network-card.j2 -D NodeName="$NodeName" -D Timestamp="$Timestamp")
	log_result "$RESULT"

	set +e
	log_debug "Start posting detection result"
	response=$(send_post "$EIS_POST_URL" "$RESULT" admin)
	# response=$(send_post "$EIS_POST_URL" "$RESULT" admin)
	ret=$?
	log_debug "$(echo "$response" | tr '\n' ' ')"
	set -e
	exit $ret
fi

if [ $ret -ne 0 ] || [ -z "$default_interfaces" ]; then
	log_err "There is no default_interface set to provider-networks"
	YAML+=$(generate_yaml_entry "provider-networks " "Not set" "There is no default_interface set to provider-networks" "error")$'\n'
	log_debug "$YAML"
	# shellcheck disable=SC2154
	RESULT=$(echo "$YAML" | jinja2 network-card.j2 -D NodeName="$NodeName" -D Timestamp="$Timestamp")
	log_result "$RESULT"

	set +e
	log_debug "Start posting detection result"
	response=$(send_post "$EIS_POST_URL" "$RESULT" admin)
	# response=$(send_post "$EIS_POST_URL" "$RESULT" admin)
	ret=$?
	log_debug "$(echo "$response" | tr '\n' ' ')"
	set -e
	exit $ret
fi

for default_interface in $default_interfaces; do
	found=false
	for bond in $bonds; do
		if [[ "$default_interface" == "$bond" ]]; then
			log_info "default_interface $default_interface found for provider-network"
			found=true
			log_info "Start checking bond subinterfaces for network card $bond"

			set +e
			log_debug "Start getting all subinterfaces for $bond"
			subinterfaces=$(get_bond_subinterfaces "$bond")
			ret=$?
			log_debug "$(echo "$subinterfaces" | tr '\n' ' ')"
			set -e

			if [ $ret -ne 0 ] || [ -z "$subinterfaces" ]; then
				log_warn "Subinterfaces not found for $bond"
				YAML+=$(generate_yaml_entry "$default_interface" "No Conflict" "" "")$'\n'
			else
				vlan_ids=""
				for subif in $subinterfaces; do
					vlan_id="${subif#*.}"
					vlan_ids+="$vlan_id "
				done
				vlan_ids=${vlan_ids%% }
				log_info "VLAN IDs for $bond: $vlan_ids"
				# TODO: 获取当前default_interface所在的provider-network所有绑定的VLAN id并对比
			fi
			break
		fi
	done
	if [[ "$found" == false ]]; then
		log_err "There is no bond found for provider-networks spec $default_interface"
		YAML+=$(generate_yaml_entry "$default_interface " "Not found" "There is no bond found for provider-networks spec $default_interface" "error")$'\n'
	fi
done

log_debug "$YAML"
# shellcheck disable=SC2154
RESULT=$(echo "$YAML" | jinja2 network-card.j2 -D NodeName="$NodeName" -D Timestamp="$Timestamp")
log_result "$RESULT"

set +e
log_debug "Start posting detection result"
response=$(send_post "$EIS_POST_URL" "$RESULT" admin)
# response=$(send_post "$EIS_POST_URL" "$RESULT" admin)
ret=$?
log_debug "$(echo "$response" | tr '\n' ' ')"
set -e
exit $ret
