local vkey, mkey = KEYS[1], KEYS[2]
local val, ttl = ARGV[1], tonumber(ARGV[2])
local field, aval, pk = ARGV[3], ARGV[4], ARGV[5]
local grpx, mpx = ARGV[6], ARGV[7]

if ttl and ttl > 0 then redis.call('SET', vkey, val, 'PX', ttl) else redis.call('SET', vkey, val) end

if field ~= '' then
  local pkey = grpx .. field .. ':' .. aval
  local oldVal = redis.call('HGET', mkey, field)
  if oldVal and oldVal ~= aval then
    redis.call('DEL', grpx .. field .. ':' .. oldVal)
  end
  local oldPk = redis.call('GET', pkey)
  if oldPk and oldPk ~= pk then
    redis.call('HDEL', mpx .. oldPk, field)
  end
  redis.call('HSET', mkey, field, aval)
  if ttl and ttl > 0 then redis.call('SET', pkey, pk, 'PX', ttl) else redis.call('SET', pkey, pk) end
end

if ttl and ttl > 0 then
  redis.call('PEXPIRE', mkey, ttl)
  local all = redis.call('HGETALL', mkey)
  for i = 1, #all, 2 do
    redis.call('PEXPIRE', grpx .. all[i] .. ':' .. all[i + 1], ttl)
  end
else
  redis.call('PERSIST', mkey)
  local all = redis.call('HGETALL', mkey)
  for i = 1, #all, 2 do
    redis.call('PERSIST', grpx .. all[i] .. ':' .. all[i + 1])
  end
end
return 1
