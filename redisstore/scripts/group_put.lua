local vkey, mkey = KEYS[1], KEYS[2]
local val = ARGV[1]
local ttl = tonumber(ARGV[2])
local pkey, fpx, mpx, vpx = ARGV[3], ARGV[4], ARGV[5], ARGV[6]

if ttl and ttl > 0 then
  redis.call('SET', vkey, val, 'PX', ttl)
else
  redis.call('SET', vkey, val)
end

if pkey ~= '' then
  local members = redis.call('SMEMBERS', mkey)
  for _, m in ipairs(members) do
    if m ~= pkey and string.sub(m, 1, string.len(fpx)) == fpx then
      redis.call('DEL', m)
      redis.call('SREM', mkey, m)
    end
  end
  local oldVk = redis.call('GET', pkey)
  if oldVk and oldVk ~= vkey then
    local oldMkey = mpx .. string.sub(oldVk, string.len(vpx) + 1)
    redis.call('SREM', oldMkey, pkey)
  end
  if ttl and ttl > 0 then
    redis.call('SET', pkey, vkey, 'PX', ttl)
  else
    redis.call('SET', pkey, vkey)
  end
  redis.call('SADD', mkey, pkey)
end

if ttl and ttl > 0 then
  redis.call('PEXPIRE', mkey, ttl)
  for _, m in ipairs(redis.call('SMEMBERS', mkey)) do
    redis.call('PEXPIRE', m, ttl)
  end
else
  redis.call('PERSIST', mkey)
  for _, m in ipairs(redis.call('SMEMBERS', mkey)) do
    redis.call('PERSIST', m)
  end
end
return 1
