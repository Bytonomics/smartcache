local pk = redis.call('GET', KEYS[1])
if not pk then return false end
local v = redis.call('GET', ARGV[1] .. pk)
if not v then return false end
return v
