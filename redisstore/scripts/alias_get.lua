local vk = redis.call('GET', KEYS[1])
if not vk then return false end
local v = redis.call('GET', vk)
if not v then return false end
return v
