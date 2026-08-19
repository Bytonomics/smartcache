local vk = redis.call('GET', KEYS[1])
if not vk then return 0 end
local mkey = ARGV[2] .. string.sub(vk, string.len(ARGV[1]) + 1)
redis.call('DEL', vk)
local members = redis.call('SMEMBERS', mkey)
for _, m in ipairs(members) do
  redis.call('DEL', m)
end
redis.call('DEL', mkey)
return 1
