local all = redis.call('HGETALL', KEYS[2])
redis.call('DEL', KEYS[1])
redis.call('DEL', KEYS[2])
return all
