redis.call('DEL', KEYS[1])
local members = redis.call('SMEMBERS', KEYS[2])
for _, m in ipairs(members) do
  redis.call('DEL', m)
end
redis.call('DEL', KEYS[2])
return 1
