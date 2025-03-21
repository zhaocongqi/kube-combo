#!/bin/bash
set -eux
echo "setup keepalived..."

TEMPLATE_CONF=temp-keepalived.conf.j2
CONF=/etc/keepalived.d/keepalived.conf
VALUES_YAML=values.yaml

# load values
if [ -z "$KEEPALIVED_VIP" ]; then
    echo "KEEPALIVED_VIP is empty."
    exit 1
fi
if [ -z "$KEEPALIVED_VIRTUAL_ROUTER_ID" ]; then
    echo "KEEPALIVED_VIRTUAL_ROUTER_ID is empty."
    exit 1
fi
if [ -z "$KEEPALIVED_NIC" ]; then
    echo "KEEPALIVED_NIC is empty."
    exit 1
fi

# # get vip mask from eth0
# intAndIP="$(ip route get 8.8.8.8 | awk '/8.8.8.8/ {print $5 "-" $7}')"
# int="${intAndIP%-*}"
# ip="${intAndIP#*-}"
# cidr="$(ip addr show dev "$int" | awk -vip="$ip" '($2 ~ ip) {print $2}')"
# mask="${cidr#*/}"
# if [ -z "$mask" ]; then
#     echo "KA_VIP_MASK is empty."
#     exit 1
# fi

printf "KA_VIP: %s\n" "${KEEPALIVED_VIP}" >"${VALUES_YAML}"
# printf "KA_VIP_MASK: %s\n" "${mask}" >> $VALUES_YAML
printf "KA_VRID: %s\n" "${KEEPALIVED_VIRTUAL_ROUTER_ID}" >>"${VALUES_YAML}"
printf "KA_NIC: %s\n" "${KEEPALIVED_NIC}" >>"${VALUES_YAML}"

random_number=$((RANDOM % 99 + 1))
printf "KA_PRIORITY: %s\n" "${random_number}" >>"${VALUES_YAML}"

cat "${VALUES_YAML}"

echo "prepare keepalived.conf..."
cp "${TEMPLATE_CONF}" keepalived.conf.j2

# use j2 to generate keepalived.conf
j2 keepalived.conf.j2 "${VALUES_YAML}" -o "${CONF}"

echo "keepalived.conf:"
cat "${CONF}"

echo "start keepalived..."
host=$(hostname)
/usr/sbin/keepalived --log-console --log-detail --dont-fork --config-id="${host}" --use-file="${CONF}"
